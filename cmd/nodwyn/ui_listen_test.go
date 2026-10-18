package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	webui "github.com/fclaude/nodwyn/internal/frontend/web"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// A wildcard --listen is refused without --name: browsers never send
// 0.0.0.0 as the host, so the console would refuse every request.
func TestWildcardListenNeedsAName(t *testing.T) {
	paths := config.PathsUnder(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	for _, address := range []string{"0.0.0.0:0", "[::]:0", ":0"} {
		r := &remoteFlags{listen: address}
		if _, _, err := r.listener(paths, webui.Options{}); err == nil || !strings.Contains(err.Error(), "--name") {
			t.Fatalf("%s: %v", address, err)
		}
	}
	r := &remoteFlags{listen: "0.0.0.0:0", hosts: []string{"desk.local:7777"}}
	l, _, err := r.listener(paths, webui.Options{PairingCode: strings.Repeat("x", 40)})
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

// The console listens on the port it had the last time, so a browser finds
// what it remembered for that origin; a port taken since gives a new one,
// kept for next time.
func TestConsoleKeepsItsPort(t *testing.T) {
	paths := config.PathsUnder(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	first, err := loopbackListener(paths)
	if err != nil {
		t.Fatal(err)
	}
	port := first.Addr().String()
	first.Close()
	again, err := loopbackListener(paths)
	if err != nil {
		t.Fatal(err)
	}
	if again.Addr().String() != port {
		t.Fatalf("listening on %s, not on %s as last time", again.Addr(), port)
	}
	// Taken now (by the listener still open): another port, kept.
	other, err := loopbackListener(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	again.Close()
	if other.Addr().String() == port {
		t.Fatal("two listeners on one port")
	}
	last, err := loopbackListener(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	if last.Addr().String() == other.Addr().String() {
		t.Fatal("a port still in use was reused")
	}
}

// A renewed certificate is served without a restart: certbot, acme.sh or a
// local CA replace the files, and the next handshake reads them. A pair that
// does not load keeps the one served before.
func TestARenewedCertificateIsServed(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	issue := func(name string) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		must.WriteFile(t, certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
		must.WriteFile(t, keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	}
	served := func(get func(*tls.ClientHelloInfo) (*tls.Certificate, error)) string {
		t.Helper()
		c, err := get(&tls.ClientHelloInfo{})
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return leaf.Subject.CommonName
	}
	issue("first")
	get, err := certificateFiles(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if name := served(get); name != "first" {
		t.Fatal(name)
	}
	// The renewed certificate's name is longer, so its file's size changes
	// even where the file system's clock is too coarse to.
	issue("renewed")
	if name := served(get); name != "renewed" {
		t.Fatalf("serving %q after the renewal", name)
	}
	must.WriteFile(t, keyFile, []byte("half written"), 0o600)
	if name := served(get); name != "renewed" {
		t.Fatalf("serving %q after a broken renewal", name)
	}
}
