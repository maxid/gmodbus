package gmodbus

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxid/gnety/codec/modbus"
	"github.com/maxid/gnety/codec/wire"
)

func TestRTUBroadcastDecodeErrorIsSilent(t *testing.T) {
	// A malformed broadcast frame must not draw an exception response: the
	// specification never answers a broadcast.
	srv := &Server{network: NetworkRTUOverTCP}
	_, respond := srv.handleServerRequest(serverRequest{
		unit:      0,
		function:  modbus.FuncReadHoldingRegisters,
		decodeErr: wire.Errorf(wire.KindMalformed, "bad request"),
	})
	if respond {
		t.Fatal("the server answered a broadcast with a decode error")
	}

	// The same error on an addressed unit is answered.
	resp, respond := srv.handleServerRequest(serverRequest{
		unit:      1,
		function:  modbus.FuncReadHoldingRegisters,
		decodeErr: wire.Errorf(wire.KindMalformed, "bad request"),
	})
	if !respond {
		t.Fatal("the server dropped an addressed decode error")
	}
	if _, ok := resp.(*modbus.Exception); !ok {
		t.Fatalf("response = %T, want an exception", resp)
	}

	// On MBAP unit 0 is an ordinary unit, so the error is answered.
	srv = &Server{network: NetworkTCP}
	if _, respond := srv.handleServerRequest(serverRequest{
		unit:      0,
		function:  modbus.FuncReadHoldingRegisters,
		decodeErr: wire.Errorf(wire.KindMalformed, "bad request"),
	}); !respond {
		t.Fatal("an MBAP unit 0 decode error was not answered")
	}
}

func TestClientRefusesBroadcastRead(t *testing.T) {
	called := make(chan struct{}, 1)
	addr := startRTUFake(t, func(frame modbus.RTUFrame) modbus.RTUFrame {
		select {
		case called <- struct{}{}:
		default:
		}
		return modbus.RTUFrame{UnitID: frame.UnitID, PDU: []byte{frame.Function(), 0x02, 0x00, 0x00}}
	})
	cli, err := NewClient(context.Background(), ClientConfig{
		Network: NetworkRTUOverTCP,
		Addr:    addr,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = cli.Close() }()

	ctx := context.Background()
	if _, err := cli.ReadHoldingRegisters(ctx, 0, 0, 1); err == nil {
		t.Fatal("broadcast read did not fail")
	} else if errors.Is(err, ErrTimeout) {
		t.Fatalf("broadcast read was sent on the wire: %v", err)
	}
	// 0x17 carries a read, so it is not broadcastable either.
	if _, err := cli.ReadWriteMultipleRegisters(ctx, 0, 0, 1, 0, []uint16{1}); err == nil {
		t.Fatal("broadcast read/write did not fail")
	}
	select {
	case <-called:
		t.Fatal("a refused broadcast reached the server")
	default:
	}
}

// errorPort always fails a read, so the serial loop must back off instead of
// spinning on the failure.
type errorPort struct {
	reads atomic.Int64
}

func (p *errorPort) Read([]byte) (int, error) {
	p.reads.Add(1)
	return 0, errors.New("port failed")
}
func (*errorPort) Write(b []byte) (int, error) { return len(b), nil }
func (*errorPort) Close() error                { return nil }
func (*errorPort) SetDeadline(time.Time) error { return nil }

func TestSerialReadFailureBacksOff(t *testing.T) {
	port := &errorPort{}
	srv, err := NewServer(context.Background(), ServerConfig{
		Network:         NetworkRTU,
		Port:            port,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	time.Sleep(250 * time.Millisecond)
	if reads := port.reads.Load(); reads <= 0 || reads > 6 {
		t.Fatalf("the serial loop read %d times in 250ms; the retry backoff is missing", reads)
	}
}

// blockingPort blocks in Read and ignores Close, which is how a wedged serial
// driver behaves.
type blockingPort struct {
	release chan struct{}
}

func (p *blockingPort) Read([]byte) (int, error) {
	<-p.release
	return 0, errors.New("released")
}
func (*blockingPort) Write(b []byte) (int, error) { return len(b), nil }
func (*blockingPort) Close() error                { return nil }
func (*blockingPort) SetDeadline(time.Time) error { return nil }

func TestServerCloseIsBounded(t *testing.T) {
	port := &blockingPort{release: make(chan struct{})}
	srv, err := NewServer(context.Background(), ServerConfig{
		Network:         NetworkRTU,
		Port:            port,
		ShutdownTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	start := time.Now()
	err = srv.Close()
	elapsed := time.Since(start)
	close(port.release)
	if err == nil {
		t.Fatal("Close returned nil with a wedged serial port")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want a deadline error", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Close took %s, want it bounded by ShutdownTimeout", elapsed)
	}
}

func TestServerCloseCancelsHandlerContext(t *testing.T) {
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	srv := newTestServer(t, func(cfg *ServerConfig) {
		cfg.ShutdownTimeout = 2 * time.Second
		cfg.Handler = HandlerFunc(func(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
			if _, ok := req.(*modbus.ReadHoldingRegistersRequest); !ok {
				return nil, ErrUnhandled
			}
			started <- struct{}{}
			<-ctx.Done()
			stopped <- struct{}{}
			return nil, ctx.Err()
		})
	})
	cli := newTestClient(t, srv.Addr(), nil)
	go func() {
		_, _ = cli.ReadHoldingRegisters(context.Background(), 1, 0, 1)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- srv.Close() }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel the handler context")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the handler stopped")
	}
}
