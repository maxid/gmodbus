package gmodbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

var errTestDialer = errors.New("test dialer refused the connection")

func TestCustomDialerIsUsed(t *testing.T) {
	srv := newFakeServer(t)
	var calls atomic.Int64
	base := &net.Dialer{}
	cli := dialForTest(t, srv, func(cfg *Config) {
		cfg.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
			calls.Add(1)
			return base.DialContext(ctx, network, addr)
		}
	})

	got, err := cli.ReadHoldingRegisters(context.Background(), 1, 0x0040, 2)
	if err != nil {
		t.Fatalf("ReadHoldingRegisters: %v", err)
	}
	if want := registerPattern(0x0040, 2, 1); !slices.Equal(got, want) {
		t.Fatalf("ReadHoldingRegisters = %v, want %v", got, want)
	}
	if calls.Load() != 1 {
		t.Fatalf("custom Dialer called %d times, want 1", calls.Load())
	}
}

func TestCustomDialerTakesPrecedenceOverTLS(t *testing.T) {
	srv := newFakeServer(t)
	var calls atomic.Int64
	base := &net.Dialer{}
	cli := dialForTest(t, srv, func(cfg *Config) {
		cfg.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
			calls.Add(1)
			return base.DialContext(ctx, network, addr)
		}
		// If TLS were applied on top of the custom dialer this handshake would
		// fail: the plain server and the empty root pool cannot satisfy it.
		cfg.TLS = &tls.Config{
			RootCAs:    x509.NewCertPool(),
			ServerName: "wrong.example",
		}
	})

	if _, err := cli.ReadHoldingRegisters(context.Background(), 1, 0, 1); err != nil {
		t.Fatalf("ReadHoldingRegisters through a custom dialer: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("custom Dialer called %d times, want 1", calls.Load())
	}
}

func TestCustomDialerErrorSurfaces(t *testing.T) {
	_, err := Dial(context.Background(), Config{
		Addr: "127.0.0.1:502",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, errTestDialer
		},
	})
	if !errors.Is(err, errTestDialer) {
		t.Fatalf("Dial = %v, want the custom Dialer error", err)
	}
}

func TestCustomDialerErrorDuringReconnectIsClassified(t *testing.T) {
	srv := newFakeServer(t)
	srv.dropRequests(func(fakeRequest) bool { return true })
	var calls atomic.Int64
	base := &net.Dialer{}
	cli := dialForTest(t, srv, func(cfg *Config) {
		cfg.Retries = 0
		cfg.Reconnect = &ReconnectPolicy{
			Delay:       time.Millisecond,
			MaxDelay:    time.Millisecond,
			MaxAttempts: 2,
		}
		cfg.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if calls.Add(1) == 1 {
				return base.DialContext(ctx, network, addr)
			}
			return nil, errTestDialer
		}
	})

	_, err := cli.ReadHoldingRegisters(context.Background(), 1, 0, 1)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadHoldingRegisters = %v, want ErrClosed", err)
	}
	if !errors.Is(err, errTestDialer) {
		t.Fatalf("ReadHoldingRegisters = %v, want the custom Dialer error", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("custom Dialer called %d times, want 3 (initial plus two reconnects)", calls.Load())
	}
	if got := srv.count(); got != 1 {
		t.Fatalf("server saw %d requests, want 1", got)
	}
	if got := srv.connectionCount(); got != 1 {
		t.Fatalf("server accepted %d connections, want 1", got)
	}
}
