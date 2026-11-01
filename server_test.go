package gmodbus

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/maxid/gnety/codec/modbus"
)

func newTestServer(t *testing.T, tune func(*ServerConfig)) *Server {
	t.Helper()
	cfg := ServerConfig{
		Network: NetworkTCP,
		Addr:    "127.0.0.1:0",
		Store:   NewMemoryStore(nil),
	}
	if tune != nil {
		tune(&cfg)
	}
	srv, err := NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func newTestClient(t *testing.T, addr string, tune func(*ClientConfig)) *Client {
	t.Helper()
	cfg := ClientConfig{Network: NetworkTCP, Addr: addr, Timeout: 2 * time.Second}
	if tune != nil {
		tune(&cfg)
	}
	cli, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestHandlerPriorityAndFallback(t *testing.T) {
	srv := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Handler = HandlerFunc(func(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
			read, ok := req.(*modbus.ReadHoldingRegistersRequest)
			if unit != 2 || !ok {
				return nil, ErrUnhandled
			}
			values := make([]uint16, read.Quantity)
			for i := range values {
				values[i] = 99
			}
			return &modbus.ReadHoldingRegistersResponse{Registers: values}, nil
		})
	})
	store := srv.store
	unit1 := store.unitFor(1)
	unit2 := store.AddUnit(2, UnitConfig{})
	if err := unit1.SetHoldingRegister(0, 5); err != nil {
		t.Fatal(err)
	}
	if err := unit2.SetHoldingRegister(0, 7); err != nil {
		t.Fatal(err)
	}
	cli := newTestClient(t, srv.Addr(), nil)

	ctx := context.Background()
	got, err := cli.ReadHoldingRegisters(ctx, 1, 0, 1)
	if err != nil {
		t.Fatalf("unit 1 read: %v", err)
	}
	if got[0] != 5 {
		t.Fatalf("unit 1 = %v, want the store's 5", got)
	}
	got, err = cli.ReadHoldingRegisters(ctx, 2, 0, 2)
	if err != nil {
		t.Fatalf("unit 2 read: %v", err)
	}
	if got[0] != 99 || got[1] != 99 {
		t.Fatalf("unit 2 = %v, want the handler's 99", got)
	}
}

func TestHandlerErrorAndPanicBecomeDeviceFailure(t *testing.T) {
	for name, handler := range map[string]Handler{
		"error": HandlerFunc(func(context.Context, uint8, modbus.Message) (modbus.Message, error) {
			return nil, errors.New("handler failed")
		}),
		"panic": HandlerFunc(func(context.Context, uint8, modbus.Message) (modbus.Message, error) {
			panic("handler exploded")
		}),
	} {
		t.Run(name, func(t *testing.T) {
			reported := make(chan struct{}, 1)
			srv := newTestServer(t, func(cfg *ServerConfig) {
				cfg.Handler = handler
				cfg.OnError = func(error) {
					select {
					case reported <- struct{}{}:
					default:
					}
				}
			})
			cli := newTestClient(t, srv.Addr(), nil)
			_, err := cli.ReadCoils(context.Background(), 1, 0, 1)
			var rejected *Error
			if !errors.As(err, &rejected) || rejected.Code != modbus.ExceptionServerDeviceFailure {
				t.Fatalf("read = %v, want ServerDeviceFailure", err)
			}
			select {
			case <-reported:
			case <-time.After(time.Second):
				t.Fatal("OnError was not called")
			}
		})
	}
}

func TestUnknownFunctionIsIllegalFunction(t *testing.T) {
	srv := newTestServer(t, nil)
	cli := newTestClient(t, srv.Addr(), nil)
	if _, err := cli.Execute(context.Background(), 1, &unknownRequest{}); err == nil {
		t.Fatal("Execute succeeded")
	} else {
		var rejected *Error
		if !errors.As(err, &rejected) || rejected.Code != modbus.ExceptionIllegalFunction {
			t.Fatalf("Execute = %v, want IllegalFunction", err)
		}
	}
}

type unknownRequest struct{}

func (*unknownRequest) Function() byte { return 0x65 }

func (*unknownRequest) PDU() ([]byte, error) { return []byte{0x65}, nil }

func TestServerMetrics(t *testing.T) {
	var (
		mu       sync.Mutex
		results  []string
		recorder = ServerMetricsFunc(func(_ byte, result string, _ time.Duration) {
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		})
	)
	srv := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Metrics = recorder
		cfg.Handler = HandlerFunc(func(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
			if unit == 2 {
				return nil, errors.New("handler failed")
			}
			return nil, ErrUnhandled
		})
	})
	unit := srv.store.unitFor(1)
	if err := unit.SetHoldingRegister(0, 1); err != nil {
		t.Fatal(err)
	}
	cli := newTestClient(t, srv.Addr(), nil)
	ctx := context.Background()
	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 1); err != nil {
		t.Fatalf("ok read: %v", err)
	}
	if _, err := cli.ReadHoldingRegisters(ctx, 1, 5000, 1); err == nil {
		t.Fatal("out-of-range read succeeded")
	}
	if _, err := cli.ReadHoldingRegisters(ctx, 2, 0, 1); err == nil {
		t.Fatal("handler error read succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{ServerResultOK, ServerResultException, ServerResultError}
	if len(results) != len(want) || results[0] != want[0] || results[1] != want[1] || results[2] != want[2] {
		t.Fatalf("results = %v, want %v", results, want)
	}
}

// ServerMetricsFunc adapts a function to ServerMetrics.
type ServerMetricsFunc func(byte, string, time.Duration)

func (f ServerMetricsFunc) Request(function byte, result string, d time.Duration) {
	f(function, result, d)
}

func TestServerMaxConnections(t *testing.T) {
	srv := newTestServer(t, func(cfg *ServerConfig) { cfg.MaxConnections = 1 })
	first := newTestClient(t, srv.Addr(), nil)
	if _, err := first.ReadCoils(context.Background(), 1, 0, 1); err != nil {
		t.Fatalf("first client: %v", err)
	}
	second := newTestClient(t, srv.Addr(), nil)
	if _, err := second.ReadCoils(context.Background(), 1, 0, 1); err == nil {
		t.Fatal("second client succeeded beyond MaxConnections")
	}
}

func TestServerIdleTimeoutIsRecoveredByReconnect(t *testing.T) {
	srv := newTestServer(t, func(cfg *ServerConfig) { cfg.IdleTimeout = 100 * time.Millisecond })
	cli := newTestClient(t, srv.Addr(), func(cfg *ClientConfig) {
		cfg.Reconnect = &ReconnectPolicy{Delay: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, MaxAttempts: 3}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.ReadCoils(ctx, 1, 0, 1); err != nil {
		t.Fatalf("first read: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := cli.ReadCoils(ctx, 1, 0, 1); err != nil {
		t.Fatalf("read after the idle close was not recovered: %v", err)
	}
}

func TestServerCloseIsIdempotent(t *testing.T) {
	srv := newTestServer(t, nil)
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := NewClient(context.Background(), ClientConfig{Addr: srv.Addr(), DialTimeout: 100 * time.Millisecond}); err == nil {
		t.Fatal("a closed server still accepts clients")
	}
}

func TestServerContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := NewServer(ctx, ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for !srv.isClosed() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !srv.isClosed() {
		t.Fatal("canceling the context did not close the server")
	}
}

func TestServerValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
	}{
		{"unknown network", ServerConfig{Network: "carrier-pigeon", Addr: ":0"}},
		{"missing address", ServerConfig{Network: NetworkTCP}},
		{"tls without config", ServerConfig{Network: NetworkTLS, Addr: "127.0.0.1:0"}},
		{"dtls without config", ServerConfig{Network: NetworkDTLS, Addr: "127.0.0.1:0"}},
		{"tls on tcp", ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0", TLS: &tls.Config{}}},
		{"port on tcp", ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0", Port: pipePort(), Store: nil}},
		{"serial on tcp", ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0", Serial: &SerialConfig{}}},
		{"nocrc on serial", ServerConfig{Network: NetworkRTU, Port: pipePort(), RTU: modbus.RTUOptions{NoCRC: true}}},
		{"negative connections", ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0", MaxConnections: -1}},
		{"negative idle", ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0", IdleTimeout: -1}},
		{"negative shutdown", ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0", ShutdownTimeout: -1}},
		{"pdu too large", ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0", MaxPDU: modbus.MaxPDU + 1}},
		{"packetconn on tcp", ServerConfig{Network: NetworkTCP, Addr: "127.0.0.1:0", PacketConn: &fakePacketConn{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if srv, err := NewServer(context.Background(), tc.cfg); err == nil {
				_ = srv.Close()
				t.Fatal("NewServer succeeded")
			}
		})
	}
}

// pipePort returns one end of an in-memory pipe as a Port, closing the other.
func pipePort() Port {
	a, b := net.Pipe()
	_ = b.Close()
	return a
}

// fakePacketConn satisfies net.PacketConn for validation tests without opening
// a socket.
type fakePacketConn struct{}

func (*fakePacketConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, net.ErrClosed }
func (*fakePacketConn) WriteTo([]byte, net.Addr) (int, error)  { return 0, net.ErrClosed }
func (*fakePacketConn) Close() error                           { return nil }
func (*fakePacketConn) LocalAddr() net.Addr                    { return &net.UDPAddr{} }
func (*fakePacketConn) SetDeadline(time.Time) error            { return nil }
func (*fakePacketConn) SetReadDeadline(time.Time) error        { return nil }
func (*fakePacketConn) SetWriteDeadline(time.Time) error       { return nil }
