package backup

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"net"
	"os/exec"
	"strings"
	"syscall"

	"github.com/hirochachacha/go-smb2/v2/x/protocol"
	"github.com/mirivlad/BackupHub/internal/scheduler"
	"github.com/pkg/sftp"
)

var (
	errStorageUnavailable  = errors.New("storage volume unavailable")
	errStorageCredentials  = errors.New("repository credentials unavailable")
	errRepositoryIntegrity = errors.New("repository integrity failure")
)

func backupFailure(ctx context.Context, code string, err error) *scheduler.BackupFailure {
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		code = "operation_timeout"
		err = errors.Join(err, context.DeadlineExceeded)
	}
	return &scheduler.BackupFailure{Code: code, Err: err}
}

func sourceFailure(ctx context.Context, err error) *scheduler.BackupFailure {
	code := "source_configuration"
	message := strings.ToLower(err.Error())
	var netErr net.Error
	var smb *protocol.ResponseError
	var sftpStatus *sftp.StatusError
	smbCode := uint32(0)
	if errors.As(err, &smb) {
		smbCode = smb.Code
	}
	sftpCode := uint32(0)
	if errors.As(err, &sftpStatus) {
		sftpCode = sftpStatus.Code
	}
	switch {
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT),
		containsFailure(message, "no space left on device", "disk quota exceeded"):
		code = "storage_full"
	case errors.Is(err, syscall.EROFS), errors.Is(err, syscall.ESTALE):
		code = "storage_unavailable"
	case errors.Is(err, fs.ErrPermission), smbCode == 0xc0000022, sftpCode == 3,
		containsFailure(message, "access_denied", "permission denied"):
		code = "source_permission_denied"
	case smbCode == 0xc000006d, smbCode == 0xc000006a,
		containsFailure(message, "unable to authenticate", "logon_failure", "authentication failed", "no password supplied", "no pg_hba.conf entry", "account_disabled", "password expired"):
		code = "source_authentication_failed"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, syscall.ETIMEDOUT),
		containsFailure(message, "timeout expired", "lock timeout", "connection timed out"):
		code = "operation_timeout"
	case errors.Is(err, fs.ErrNotExist), smbCode == 0xc00000cc, smbCode == 0xc0000034, smbCode == 0xc000003a, sftpCode == 2,
		containsFailure(message, "bad_network_name", "object_name_not_found", "object_path_not_found"):
		code = "source_unavailable"
	case errors.As(err, &netErr), errors.Is(err, net.ErrClosed), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, sftp.ErrSSHFxConnectionLost), errors.Is(err, sftp.ErrSSHFxNoConnection), sftpCode == 6, sftpCode == 7,
		containsFailure(message, "connection refused", "connection reset", "network is unreachable", "no route to host", "server closed the connection", "ssl syscall error: eof detected", "could not receive data from server", "lost connection", "connection to server was lost", "connection unexpectedly closed", "nt_status_connection_disconnected", "nt_status_io_timeout", "broken pipe", "could not translate host name", "transport is closing"):
		code = "network_unavailable"
	case containsFailure(message, "refusing incomplete snapshot", "capture is incomplete"):
		code = "capture_failed"
	}
	return backupFailure(ctx, code, err)
}

func storageFailure(ctx context.Context, err error) *scheduler.BackupFailure {
	code := "storage_configuration"
	switch {
	case errors.Is(err, errStorageCredentials), errors.Is(err, fs.ErrPermission):
		code = "storage_authentication_failed"
	case errors.Is(err, errRepositoryIntegrity):
		code = "repository_integrity"
	case errors.Is(err, errStorageUnavailable), errors.Is(err, syscall.EIO), errors.Is(err, syscall.ESTALE), errors.Is(err, syscall.EROFS),
		errors.Is(err, fs.ErrNotExist) && !errors.Is(err, exec.ErrNotFound):
		code = "storage_unavailable"
	case errors.Is(err, sql.ErrNoRows):
		code = "storage_configuration"
	}
	return backupFailure(ctx, code, err)
}

func resticStageFailure(ctx context.Context, code string, err error) *scheduler.BackupFailure {
	err = resticLockError(err)
	message := strings.ToLower(err.Error())
	var exit *exec.ExitError
	switch {
	case errors.Is(err, errResticRepositoryLocked):
		code = "restic_repository_locked"
	case subprocessInterrupted(err), containsFailure(message, "signal interrupt received", "signal terminated received"):
		code = "restic_interrupted"
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT),
		containsFailure(message, "no space left on device", "disk quota exceeded"):
		code = "storage_full"
	case errors.Is(err, errStorageCredentials), errors.As(err, &exit) && exit.ExitCode() == 12,
		containsFailure(message, "wrong password", "no key found", "permission denied", "access denied"):
		code = "storage_authentication_failed"
	case errors.Is(err, errRepositoryIntegrity),
		containsFailure(message, "does not match", "hash mismatch", "invalid data returned", "ciphertext verification failed", "invalid repository config", "tree blob", "snapshot archive incomplete", "pack file cannot be read", "pack id does not match", "blob not found", "snapshot archive has a missing or invalid end marker"):
		code = "repository_integrity"
	case errors.Is(err, errStorageUnavailable), errors.Is(err, syscall.EIO), errors.Is(err, syscall.EROFS), errors.Is(err, syscall.ESTALE),
		containsFailure(message, "input/output error", "remote i/o error", "read-only file system", "stale file handle", "unable to open config file", "no such device"):
		code = "storage_unavailable"
	}
	return backupFailure(ctx, code, err)
}

func subprocessInterrupted(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	status, ok := exit.ProcessState.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}

func (s *Service) resticFailure(ctx context.Context, repoID, code string, err error) *scheduler.BackupFailure {
	failure := resticStageFailure(ctx, code, err)
	// A mount can vanish after preflight. Restic reports filesystem errors as
	// process output, so recheck availability before treating those as final.
	if failure.Code == code && ctx.Err() == nil {
		if _, unavailable := s.repositoryFor(ctx, repoID); unavailable != nil {
			check := storageFailure(ctx, unavailable)
			if check.Code == "storage_unavailable" {
				return check
			}
		}
	}
	return failure
}

func containsFailure(message string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}
