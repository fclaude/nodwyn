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
	"os"
	"path/filepath"
	"slices"
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

// --acme has the console get its own certificate (ADR 30, amended
// 2026-10-17): it takes a host name, the CA's terms said yes to, and no
// certificate files; it answers on :443 to the name given, which the console
// then answers to, and pairs devices as any console other machines reach.
func TestAcmeFlagsAreCheckedAndSetTheConsoleUp(t *testing.T) {
	paths := config.PathsUnder(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	for name, r := range map[string]*remoteFlags{
		"no terms":               {acme: "nodwyn.example.net"},
		"an address":             {acme: "203.0.113.5", acmeAccept: true},
		"a name with a port":     {acme: "nodwyn.example.net:443", acmeAccept: true},
		"a name with no dots":    {acme: "nodwyn", acmeAccept: true},
		"with certificate files": {acme: "nodwyn.example.net", acmeAccept: true, cert: "c.pem", key: "k.pem"},
	} {
		if _, _, err := r.listener(paths, webui.Options{PairingCode: strings.Repeat("x", 40)}); err == nil {
			t.Fatalf("%s: taken", name)
		}
	}
	// :443 is not bound here, so another address stands in for it.
	r := &remoteFlags{acme: "nodwyn.example.net", acmeAccept: true, listen: "127.0.0.1:0"}
	l, options, err := r.listener(paths, webui.Options{PairingCode: strings.Repeat("x", 40)})
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if !options.TLS || options.Devices == nil || !slices.Equal(options.Hosts, []string{"nodwyn.example.net"}) {
		t.Fatalf("options: %+v", options)
	}
	if !r.shared() || (&remoteFlags{}).shared() {
		t.Fatal("shared")
	}
	if info, err := os.Stat(filepath.Join(paths.ConfigDir, "acme")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the certificates' directory: %v %v", info, err)
	}
}

// The ACME configuration answers only the name it was given, answers the
// CA's TLS-ALPN-01 check on the same port, and serves a certificate it
// already holds with no word to the CA (the directory here is a closed
// port): a restart, or the CA being down, costs nothing.
func TestAcmeConfigServesItsNameFromTheCacheAndNoOther(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "acme")
	config, err := acmeConfig("nodwyn.example.net", "", "http://127.0.0.1:1/directory", cache)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(config.NextProtos, "acme-tls/1") || config.MinVersion != tls.VersionTLS12 {
		t.Fatalf("config: %v %x", config.NextProtos, config.MinVersion)
	}
	if info, err := os.Stat(cache); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache: %v %v", info, err)
	}
	// A name that was not given asks the CA for nothing.
	if _, err := config.GetCertificate(&tls.ClientHelloInfo{ServerName: "evil.example.net"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("another name: %v", err)
	}
	// A certificate held already, made as the CA would.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "nodwyn.example.net"}, DNSNames: []string{"nodwyn.example.net"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	held := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	must.WriteFile(t, filepath.Join(cache, "nodwyn.example.net"), held, 0o600)
	// A browser's hello, which offers ECDSA, the key this one has.
	hello := &tls.ClientHelloInfo{ServerName: "nodwyn.example.net", SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256, tls.PSSWithSHA256},
		SupportedCurves: []tls.CurveID{tls.CurveP256}, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12}}
	got, err := config.GetCertificate(hello)
	if err != nil {
		t.Fatalf("the certificate held: %v", err)
	}
	leaf, err := x509.ParseCertificate(got.Certificate[0])
	if err != nil || leaf.SerialNumber.Int64() != 7 {
		t.Fatalf("served %v %v", leaf, err)
	}
}
