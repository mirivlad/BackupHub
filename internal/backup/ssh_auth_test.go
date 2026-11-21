package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func makeSSHKey(t *testing.T, passphrase string) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "backuphub-test", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "backuphub-test")
	}
	if err != nil {
		t.Fatal(err)
	}
	pemString := string(pem.EncodeToMemory(block))
	signer, err := parseSSHKey(pemString, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	return pemString, signer
}
func TestSSHPrivateKeysWithAndWithoutPassphrase(t *testing.T) {
	for _, passphrase := range []string{"", "test passphrase"} {
		priv, signer := makeSSHKey(t, passphrase)
		if _, err := parseSSHKey(priv, passphrase); err != nil {
			t.Fatal(err)
		}
		if passphrase != "" {
			if _, err := parseSSHKey(priv, "wrong passphrase"); err == nil {
				t.Fatal("accepted incorrect key passphrase")
			}
		}
		if signer.PublicKey() == nil {
			t.Fatal("missing public key")
		}
		data, err := sshStoredSecret(CreateSource{Kind: "sftp", AuthMethod: "key", PrivateKey: priv, KeyPassphrase: passphrase})
		if err != nil {
			t.Fatal(err)
		}
		auth, err := sshAuthentication(Settings{AuthMethod: "key"}, data)
		if err != nil || len(auth) != 1 {
			t.Fatalf("key auth failed: %v", err)
		}
		if strings.Contains(data, "wrong passphrase") {
			t.Fatal("wrong passphrase stored")
		}
	}
}
func TestTOFUPinningAndChangedHostKeyRejected(t *testing.T) {
	requireIntegrationRestic(t)
	svc := serviceFixture(t)
	ctx := context.Background()
	priv, signer := makeSSHKey(t, "")
	src, err := svc.Create(ctx, CreateSource{
		Name: "remote", Kind: "sftp", Host: "example.org:2222", Username: "backup",
		Path: "/srv/backup", AuthMethod: "key", PrivateKey: priv,
	})
	if err != nil {
		t.Fatal(err)
	}
	callback := svc.sshHostKeyCallback(ctx, src.ID, "example.org:2222")
	if err = callback("example.org:2222", nil, signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err = callback("example.org:2222", nil, signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListSources(ctx)
	if err != nil || len(list) != 1 || list[0].HostKeyFingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("fingerprint not saved: %+v %v", list, err)
	}
	_, changed := makeSSHKey(t, "")
	if err = callback("example.org:2222", nil, changed.PublicKey()); err == nil {
		t.Fatal("accepted changed server key")
	}
	if err = callback("other.example.org:2222", nil, signer.PublicKey()); err == nil {
		t.Fatal("accepted destination mismatch")
	}
	// A deliberate edit of hostname permits first-use pinning for the NEW host.
	_, err = svc.UpdateSource(ctx, src.ID, CreateSource{Name: "remote", Kind: "sftp", Host: "new.example.org:2222", Username: "backup", Path: "/srv/backup", AuthMethod: "key"})
	if err != nil {
		t.Fatal(err)
	}
	list, err = svc.ListSources(ctx)
	if err != nil || list[0].HostKeyFingerprint != "" {
		t.Fatalf("host change did not reset trust %+v %v", list, err)
	}
	next := svc.sshHostKeyCallback(ctx, src.ID, "new.example.org:2222")
	if err = next("new.example.org:2222", nil, changed.PublicKey()); err != nil {
		t.Fatal(err)
	}
}
func TestSSHKeyNeverReturnedInSourceListing(t *testing.T) {
	requireIntegrationRestic(t)
	svc := serviceFixture(t)
	ctx := context.Background()
	priv, _ := makeSSHKey(t, "hidden phrase")
	src, err := svc.Create(ctx, CreateSource{Name: "secret", Kind: "sftp", Host: "test.example", Username: "tester", Path: "/var/log", AuthMethod: "key", PrivateKey: priv, KeyPassphrase: "hidden phrase"})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := svc.ListSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].ID != src.ID {
		t.Fatal(err)
	}
	if strings.Contains(sources[0].Host, "PRIVATE KEY") || strings.Contains(sources[0].Path, "hidden phrase") {
		t.Fatal("secret leaked")
	}
}
func TestLegacySSHPasswordPreservedAcrossEditing(t *testing.T) {
	requireIntegrationRestic(t)
	svc := serviceFixture(t)
	ctx := context.Background()
	// A v0.2.1 source has an empty auth_method in its stored JSON.
	src, err := svc.Create(ctx, CreateSource{Name: "legacy", Kind: "sftp", Host: "old.example", Username: "admin", Path: "/var/backups", Password: "original-ssh-password"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an existing source serialized before auth_method existed.
	_, err = svc.catalog.DB.ExecContext(ctx, "UPDATE sources SET settings_json=? WHERE id=?", `{"Host":"old.example","Share":"","Path":"/var/backups","Username":"admin","Database":""}`, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, oldSettings, ref, err := svc.source(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldSettings.AuthMethod != "" {
		t.Fatalf("expected legacy source: %+v", oldSettings)
	}
	before, err := svc.vault.Get(ctx, ref)
	if err != nil || string(before) != "original-ssh-password" {
		t.Fatal("initial password lost")
	}
	list, err := svc.ListSources(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	if list[0].AuthMethod != "" {
		t.Fatal("legacy auth method changed unexpectedly")
	}
	_, err = svc.UpdateSource(ctx, src.ID, CreateSource{Name: "Legacy renamed", Kind: "sftp", Host: "old.example", Username: "admin", Path: "/var/backups", AuthMethod: "password"})
	if err != nil {
		t.Fatal(err)
	}
	_, settings, afterRef, err := svc.source(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := svc.vault.Get(ctx, afterRef)
	if err != nil || string(secret) != "original-ssh-password" {
		t.Fatalf("password vanished after edit: %v", err)
	}
	if settings.AuthMethod != "password" {
		t.Fatal("password method was not saved")
	}
	_, err = sshAuthentication(settings, string(secret))
	if err != nil {
		t.Fatal(err)
	}
}
