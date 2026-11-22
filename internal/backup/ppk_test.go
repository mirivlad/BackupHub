package backup

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPuTTYImportThroughSourceCreateAndUpdate(t *testing.T) {
	requireIntegrationRestic(t)
	puttygen, err := exec.LookPath("puttygen")
	if err != nil {
		t.Skip("puttygen not installed")
	}
	ctx := context.Background()
	service := serviceFixture(t)
	dir := t.TempDir()
	passfile := filepath.Join(dir, "passphrase")
	const passphrase = "fixture-passphrase"
	for _, fixture := range []struct{ version, passphrase string }{{"2", passphrase}, {"3", ""}} {
		if err = os.WriteFile(passfile, []byte(fixture.passphrase+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		filename := filepath.Join(dir, "key-"+fixture.version+".ppk")
		if output, err := exec.Command(puttygen, "-t", "ed25519", "-q", "--ppk-param", "version="+fixture.version,
			"--new-passphrase", passfile, "-o", filename).CombinedOutput(); err != nil {
			t.Fatalf("generate PPK fixture: %v %s", err, output)
		}
		raw, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		input := CreateSource{
			Name: "PuTTY " + fixture.version, Kind: "sftp", Host: "files.example", Path: "/backup", Username: "backup",
			AuthMethod: "key", PrivateKey: string(raw), KeyPassphrase: fixture.passphrase,
		}
		if fixture.passphrase != "" {
			bad := input
			bad.KeyPassphrase = "wrong-passphrase"
			if _, err = service.Create(ctx, bad); err == nil {
				t.Fatal("accepted an invalid PuTTY passphrase")
			}
		}
		src, err := service.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		input.Name += " updated"
		if _, err = service.UpdateSource(ctx, src.ID, input); err != nil {
			t.Fatal(err)
		}
		_, settings, ref, err := service.source(ctx, src.ID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := service.vault.Get(ctx, ref)
		if err != nil || !bytes.Contains(data, []byte("-----BEGIN OPENSSH PRIVATE KEY-----")) {
			t.Fatalf("expected a converted key in the vault: %v", err)
		}
		if _, err = sshAuthentication(settings, string(data)); err != nil {
			t.Fatalf("cannot use imported key for SSH: %v", err)
		}
	}
}
