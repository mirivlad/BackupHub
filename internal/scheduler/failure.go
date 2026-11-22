package scheduler

import (
	"context"
	"errors"
)

var ErrSourceUnavailable = errors.New("source unavailable")
var ErrSourcePermissionDenied = errors.New("source permission denied")
var ErrSSHHostKeyChanged = errors.New("SSH host key changed")

// BackupFailure carries a component-selected code. Only the centrally
// enumerated transient failures spend the retry budget.
type BackupFailure struct {
	Code string
	Err  error
}

func (e *BackupFailure) Error() string { return e.Err.Error() }
func (e *BackupFailure) Unwrap() error { return e.Err }

func failureCode(err error) string {
	var failure *BackupFailure
	switch {
	case errors.Is(err, ErrSSHHostKeyChanged):
		return "ssh_host_key_changed"
	case errors.Is(err, ErrSourcePermissionDenied):
		return "source_permission_denied"
	case errors.As(err, &failure):
		return failure.Code
	case errors.Is(err, ErrSourceUnavailable):
		return "source_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation_timeout"
	default:
		return "executor_error"
	}
}

func retryable(code string) bool {
	switch code {
	case "source_unavailable", "storage_unavailable", "storage_full", "network_unavailable",
		"operation_timeout", "process_restart", "snapshot_catalog_failed", "restic_interrupted":
		return true
	default:
		return false
	}
}

// FailureDetail is safe to display to all catalog readers. Raw Restic and
// connector diagnostics can contain credentials or sensitive paths.
func FailureDetail(code string) string {
	switch code {
	case "source_unavailable":
		return "Источник временно недоступен."
	case "storage_unavailable":
		return "Целевое хранилище недоступно."
	case "storage_full":
		return "Недостаточно места на целевом диске."
	case "network_unavailable":
		return "Соединение с источником прервано."
	case "operation_timeout":
		return "Истекло время ожидания подключения или прогресса операции."
	case "source_permission_denied", "source_authentication_failed", "storage_authentication_failed":
		return "Проверьте учётные данные и права доступа."
	case "ssh_host_key_changed":
		return "Изменился доверенный ключ SSH-сервера."
	case "repository_integrity":
		return "Restic обнаружил нарушение целостности. Требуется проверка репозитория."
	case "restic_repository_locked":
		return "Репозиторий заблокирован Restic. Проверьте активные операции и блокировку."
	case "process_restart":
		return "Попытка прервана при остановке службы."
	case "restic_interrupted":
		return "Процесс Restic завершился во время попытки."
	case "snapshot_catalog_failed":
		return "Не удалось завершить запись в каталог SQLite."
	case "restic_partial_backup":
		return "Restic прочитал только часть источника."
	case "source_configuration", "storage_configuration":
		return "Проверьте настройки и доступность необходимых программ."
	case "retry_exhausted":
		return "Лимит повторных попыток исчерпан."
	case "":
		return ""
	default:
		return "Не удалось завершить этап резервного копирования."
	}
}
