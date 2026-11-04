package gmodbus

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"strings"
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

func TestServerMetricsOnPanic(t *testing.T) {
	results := make(chan string, 4)
	srv := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Metrics = ServerMetricsFunc(func(_ byte, result string, _ time.Duration) {
			results <- result
		})
		cfg.Handler = HandlerFunc(func(context.Context, uint8, modbus.Message) (modbus.Message, error) {
			panic("boom")
		})
	})
	cli := newTestClient(t, srv.Addr(), nil)
	if _, err := cli.ReadCoils(context.Background(), 1, 0, 1); err == nil {
		t.Fatal("read succeeded")
	}
	select {
	case result := <-results:
		if result != ServerResultError {
			t.Fatalf("result = %q, want %q", result, ServerResultError)
		}
	case <-time.After(time.Second):
		t.Fatal("a panicking handler produced no metric")
	}
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
		{"listener on udp", ServerConfig{Network: NetworkUDP, Addr: "127.0.0.1:0", Listener: &fakeListener{}}},
		{"listener on rtu-udp", ServerConfig{Network: NetworkRTUOverUDP, Addr: "127.0.0.1:0", Listener: &fakeListener{}}},
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

// fakeListener satisfies net.Listener for validation tests without binding a
// socket.
type fakeListener struct{}

func (*fakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*fakeListener) Close() error              { return nil }
func (*fakeListener) Addr() net.Addr            { return &net.TCPAddr{} }

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

// TestStreamDisconnectIsNotReported checks that a client which closes its
// connection — at a frame boundary or in the middle of a frame — does not reach
// OnError, while a frame that arrives complete and malformed still does. The
// second half is the control: without it, a server that simply stopped reading
// would pass the first.
func TestStreamDisconnectIsNotReported(t *testing.T) {
	reported := make(chan error, 4)
	srv := newTestServer(t, func(cfg *ServerConfig) {
		cfg.OnError = func(err error) {
			select {
			case reported <- err:
			default:
			}
		}
	})

	// A partial MBAP header, then the socket closes: the frame never completes.
	partial, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("dial partial: %v", err)
	}
	if _, err := partial.Write([]byte{0x00, 0x01, 0x00}); err != nil {
		t.Fatalf("write partial frame: %v", err)
	}
	_ = partial.Close()

	// Nothing at all, then close: a clean EOF at a frame boundary.
	silent, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("dial silent: %v", err)
	}
	_ = silent.Close()

	select {
	case err := <-reported:
		t.Fatalf("OnError got %v for a client that just disconnected", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Control: a complete frame with a protocol id that is not Modbus is a broken
	// peer, not a departure, and must still be reported.
	broken, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("dial broken: %v", err)
	}
	defer func() { _ = broken.Close() }()
	if _, err := broken.Write([]byte{0x00, 0x01, 0x00, 0xff, 0x00, 0x02, 0x01, 0x03}); err != nil {
		t.Fatalf("write malformed frame: %v", err)
	}
	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "read request") {
			t.Fatalf("OnError got %v, want a read request error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnError was not called for a malformed frame")
	}
}

// TestExceptionFormRequestIsAnswered checks that a request whose function code
// already has the exception bit set (0x83, which no request may use) is answered
// with a legal IllegalFunction exception rather than dropped on the floor: the
// answer's function code is the masked base with the bit set again by the encoder.
func TestExceptionFormRequestIsAnswered(t *testing.T) {
	srv := newTestServer(t, nil)
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// MBAP: transaction 1, protocol 0, length 3 (unit id plus the two-byte PDU).
	request := []byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x03, 0x01, 0x83, 0x02}
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	frame, err := modbus.Read(bufio.NewReader(conn), 0)
	if err != nil {
		t.Fatalf("read answer: %v", err)
	}
	want := []byte{0x83, byte(modbus.ExceptionIllegalFunction)}
	if !slices.Equal(frame.PDU, want) {
		t.Fatalf("answer PDU = %x, want %x", frame.PDU, want)
	}
	if frame.TransactionID != 1 || frame.UnitID != 1 {
		t.Fatalf("answer transaction %d unit %d, want 1/1", frame.TransactionID, frame.UnitID)
	}
}
