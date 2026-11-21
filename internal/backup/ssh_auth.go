package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/mirivlad/BackupHub/internal/scheduler"
	"golang.org/x/crypto/ssh"
)

type sshKeySecret struct {
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase,omitempty"`
}

func parseSSHKey(pem, passphrase string) (ssh.Signer, error) {
	if len(pem) == 0 || len(pem) > 65536 || len(passphrase) > 4096 {
		return nil, errors.New("invalid SSH private key size")
	}
	if passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase([]byte(pem), []byte(passphrase))
		if err != nil {
			return nil, errors.New("SSH private key or passphrase is invalid")
		}
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey([]byte(pem))
	if err != nil {
		return nil, errors.New("SSH private key is invalid or requires a passphrase")
	}
	return signer, nil
}
func sshStoredSecret(in CreateSource) (string, error) {
	if in.Kind != "sftp" || in.AuthMethod != "key" {
		return in.Password, nil
	}
	if _, err := parseSSHKey(in.PrivateKey, in.KeyPassphrase); err != nil {
		return "", err
	}
	data, err := json.Marshal(sshKeySecret{PrivateKey: in.PrivateKey, Passphrase: in.KeyPassphrase})
	return string(data), err
}
func sshAuthentication(settings Settings, secret string) ([]ssh.AuthMethod, error) {
	if settings.AuthMethod == "" || settings.AuthMethod == "password" {
		if secret == "" {
			return nil, errors.New("SSH password is missing")
		}
		return []ssh.AuthMethod{ssh.Password(secret)}, nil
	}
	if settings.AuthMethod != "key" {
		return nil, errors.New("unknown SSH authentication mode")
	}
	var cred sshKeySecret
	if err := json.Unmarshal([]byte(secret), &cred); err != nil {
		return nil, errors.New("SSH private key cannot be decoded")
	}
	signer, err := parseSSHKey(cred.PrivateKey, cred.Passphrase)
	if err != nil {
		return nil, err
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

// The host key is automatically pinned once per source on the FIRST SSH
// handshake (TOFU), never silently overwritten on subsequent connections.
// Users can independently verify the saved SHA256 fingerprint in the UI.
func (s *Service) sshHostKeyCallback(ctx context.Context, sourceID, expectedAddress string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if hostname != expectedAddress {
			return errors.New("SSH destination mismatch")
		}
		fingerprint := ssh.FingerprintSHA256(key)
		blob := key.Marshal()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err := s.catalog.DB.ExecContext(ctx, `INSERT INTO ssh_trusted_hosts(source_id,server_address,public_key,fingerprint,first_seen)
    VALUES(?,?,?,?,?) ON CONFLICT(source_id) DO NOTHING`, sourceID, expectedAddress, blob, fingerprint, now)
		if err != nil {
			return fmt.Errorf("cannot store first SSH server key: %w", err)
		}
		var storedAddress string
		var storedKey []byte
		err = s.catalog.DB.QueryRowContext(ctx, `SELECT server_address,public_key FROM ssh_trusted_hosts WHERE source_id=?`, sourceID).
			Scan(&storedAddress, &storedKey)
		if err != nil {
			return err
		}
		if storedAddress != expectedAddress || !bytes.Equal(storedKey, blob) {
			return fmt.Errorf("%w: connection blocked; verify server identity before accepting a replacement", scheduler.ErrSSHHostKeyChanged)
		}
		return nil
	}
}

// connectSSH uses the normal SSH handshake; TOFU is pinned before any user
// credentials are sent. A changed host key aborts authentication.
func (s *Service) connectSSH(ctx context.Context, sourceID string, settings Settings, secret string) (*ssh.Client, error) {
	address := settings.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "22")
	}
	auth, err := sshAuthentication(settings, secret)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User: settings.Username, Auth: auth, HostKeyCallback: s.sshHostKeyCallback(ctx, sourceID, address), Timeout: 15 * time.Second,
	}
	socket, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("SSH connection failed: %w", err)
	}
	sshConn, chans, requests, err := ssh.NewClientConn(socket, address, config)
	if err != nil {
		_ = socket.Close()
		return nil, fmt.Errorf("SSH authentication or trusted host key check failed: %w", err)
	}
	return ssh.NewClient(sshConn, chans, requests), nil
}
