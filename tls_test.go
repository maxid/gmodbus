package gmodbus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"slices"
	"testing"
	"time"
)

// testCertificate generates a self-signed certificate for 127.0.0.1 and
// localhost, plus the pool that trusts it.
func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gmodbus test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, pool
}

func TestTLSConnection(t *testing.T) {
	cert, pool := testCertificate(t)
	srv := newTLSFakeServer(t, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	cli := dialForTest(t, srv, func(cfg *Config) {
		cfg.TLS = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	})

	got, err := cli.ReadHoldingRegisters(context.Background(), 1, 0x0030, 3)
	if err != nil {
		t.Fatalf("ReadHoldingRegisters over TLS: %v", err)
	}
	if want := registerPattern(0x0030, 3, 1); !slices.Equal(got, want) {
		t.Fatalf("ReadHoldingRegisters over TLS = %v, want %v", got, want)
	}
	if got := srv.count(); got != 1 {
		t.Fatalf("server saw %d requests, want 1", got)
	}
}

func TestTLSReconnectsAfterDroppedConnection(t *testing.T) {
	cert, pool := testCertificate(t)
	srv := newTLSFakeServer(t, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	srv.dropRequests(func(req fakeRequest) bool { return req.index == 0 })
	cli := dialForTest(t, srv, func(cfg *Config) {
		cfg.TLS = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		cfg.Reconnect = &ReconnectPolicy{
			Delay:       time.Millisecond,
			MaxDelay:    2 * time.Millisecond,
			MaxAttempts: 2,
		}
	})

	got, err := cli.ReadHoldingRegisters(context.Background(), 1, 0x0030, 2)
	if err != nil {
		t.Fatalf("ReadHoldingRegisters over a reconnected TLS session: %v", err)
	}
	if want := registerPattern(0x0030, 2, 1); !slices.Equal(got, want) {
		t.Fatalf("ReadHoldingRegisters = %v, want %v", got, want)
	}
	if got := srv.connectionCount(); got != 2 {
		t.Fatalf("server accepted %d TLS connections, want 2", got)
	}
}

func TestTLSConfigurationErrorIsClassified(t *testing.T) {
	cert, _ := testCertificate(t)
	srv := newTLSFakeServer(t, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})

	// An empty pool trusts no CA, so the self-signed certificate cannot verify.
	_, err := Dial(context.Background(), Config{
		Addr: srv.addr(),
		TLS:  &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12},
	})
	if !errors.Is(err, ErrTLSConfig) {
		t.Fatalf("Dial = %v, want ErrTLSConfig", err)
	}
	if errors.Is(err, ErrTLSHandshake) {
		t.Errorf("Dial error %v is classified as transient as well", err)
	}
	if got := srv.count(); got != 0 {
		t.Fatalf("server saw %d requests after a rejected handshake, want 0", got)
	}
}

func TestTLSTransientFailureIsClassified(t *testing.T) {
	_, pool := testCertificate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			// Closing at once makes the handshake fail with EOF, a transient
			// transport error rather than a certificate problem.
			_ = conn.Close()
			accepted <- conn
		}
	}()

	_, err = Dial(context.Background(), Config{
		Addr:        ln.Addr().String(),
		DialTimeout: time.Second,
		TLS:         &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	})
	if !errors.Is(err, ErrTLSHandshake) {
		t.Fatalf("Dial = %v, want ErrTLSHandshake", err)
	}
	if errors.Is(err, ErrTLSConfig) {
		t.Errorf("Dial error %v is classified as permanent as well", err)
	}

	// Close the peer that made the handshake fail and let the accept goroutine
	// finish.
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("server never accepted the connection")
	}
}

func TestTLSHandshakeHonorsDialTimeout(t *testing.T) {
	_, pool := testCertificate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	start := time.Now()
	_, err = Dial(context.Background(), Config{
		Addr:        ln.Addr().String(),
		DialTimeout: 50 * time.Millisecond,
		TLS:         &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTLSHandshake) {
		t.Fatalf("Dial = %v, want ErrTLSHandshake", err)
	}
	if elapsed < 30*time.Millisecond {
		t.Errorf("TLS handshake returned after %s, before the 50ms DialTimeout", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("TLS handshake took %s, want it bounded by DialTimeout", elapsed)
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("server never accepted the connection")
	}
}
