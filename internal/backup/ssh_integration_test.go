package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRealSSHHandshakeWithEncryptedPrivateKeyAndTOFU(t *testing.T) {
	svc := serviceFixture(t)
	ctx := context.Background()
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clientPEM, clientSigner := makeSSHKey(t, "testing strong passphrase")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "test-user" || string(key.Marshal()) != string(clientSigner.PublicKey().Marshal()) {
				return nil, ssh.ErrNoAuth
			}
			return nil, nil
		},
	}
	server.AddHostKey(hostSigner)
	accepted := make(chan bool, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			accepted <- false
			return
		}
		defer c.Close()
		conn, chans, requests, e := ssh.NewServerConn(c, server)
		if e != nil {
			accepted <- false
			return
		}
		accepted <- true
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		for c := range chans {
			_ = c.Reject(ssh.Prohibited, "no channels in this test")
		}
	}()
	src, err := svc.Create(ctx, CreateSource{
		Name: "remote SSH", Kind: "sftp", Host: listener.Addr().String(),
		Username: "test-user", Path: "/srv/test",
		AuthMethod: "key", PrivateKey: clientPEM, KeyPassphrase: "testing strong passphrase",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, settings, ref, err := svc.source(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := svc.vault.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := svc.connectSSH(ctx, src.ID, settings, string(data))
	if err != nil {
		t.Fatal(err)
	}
	if connection.User() != "test-user" {
		t.Fatalf("authenticated as %q", connection.User())
	}
	_ = connection.Close()
	select {
	case ok := <-accepted:
		if !ok {
			t.Fatal("SSH server did not accept credentials")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSH handshake timeout")
	}
	sources, err := svc.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := sources[0].HostKeyFingerprint; got != ssh.FingerprintSHA256(hostSigner.PublicKey()) {
		t.Fatalf("unexpected host key fingerprint %q", got)
	}
}
