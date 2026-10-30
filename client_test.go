package gmodbus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/maxid/gnety/codec/modbus"
)

// dialForTest connects a client to the fake server and closes it at the end of
// the test.
func dialForTest(t *testing.T, srv *fakeServer, tune func(*Config)) *Client {
	t.Helper()
	cfg := Config{Addr: srv.addr(), Timeout: 2 * time.Second}
	if tune != nil {
		tune(&cfg)
	}
	cli, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// assertRequest checks the request the server decoded against the one the test
// expected. The comparison uses the encoded PDU, so a pointer and a value form
// of the same request are equivalent.
func assertRequest(t *testing.T, req fakeRequest, unit uint8, want modbus.Message) {
	t.Helper()
	if req.unit != unit {
		t.Errorf("server saw unit %d, want %d", req.unit, unit)
	}
	wantPDU, err := want.PDU()
	if err != nil {
		t.Fatalf("encode expected request: %v", err)
	}
	gotPDU, err := req.msg.PDU()
	if err != nil {
		t.Fatalf("re-encode decoded request %#v: %v", req.msg, err)
	}
	if req.function != wantPDU[0] {
		t.Errorf("server saw function %#02x, want %#02x", req.function, wantPDU[0])
	}
	if !bytes.Equal(gotPDU, wantPDU) {
		t.Errorf("server decoded %#v (% x), want %#v (% x)", req.msg, gotPDU, want, wantPDU)
	}
}

func TestOperations(t *testing.T) {
	ctx := context.Background()

	t.Run("ReadCoils", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, nil)

		got, err := cli.ReadCoils(ctx, 3, 0x0010, 10)
		if err != nil {
			t.Fatalf("ReadCoils: %v", err)
		}
		if want := coilPattern(0x0010, 10); !slices.Equal(got, want) {
			t.Fatalf("ReadCoils = %v, want %v", got, want)
		}
		reqs := srv.seen()
		if len(reqs) != 1 {
			t.Fatalf("server saw %d requests, want 1", len(reqs))
		}
		assertRequest(t, reqs[0], 3, &modbus.ReadCoilsRequest{Address: 0x0010, Quantity: 10})
	})

	t.Run("ReadDiscreteInputs", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, nil)

		got, err := cli.ReadDiscreteInputs(ctx, 4, 0x0020, 9)
		if err != nil {
			t.Fatalf("ReadDiscreteInputs: %v", err)
		}
		if want := inputPattern(0x0020, 9); !slices.Equal(got, want) {
			t.Fatalf("ReadDiscreteInputs = %v, want %v", got, want)
		}
		reqs := srv.seen()
		if len(reqs) != 1 {
			t.Fatalf("server saw %d requests, want 1", len(reqs))
		}
		assertRequest(t, reqs[0], 4, &modbus.ReadDiscreteInputsRequest{Address: 0x0020, Quantity: 9})
	})

	t.Run("ReadHoldingRegisters", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, nil)

		got, err := cli.ReadHoldingRegisters(ctx, 5, 0x0030, 3)
		if err != nil {
			t.Fatalf("ReadHoldingRegisters: %v", err)
		}
		if want := registerPattern(0x0030, 3, 1); !slices.Equal(got, want) {
			t.Fatalf("ReadHoldingRegisters = %v, want %v", got, want)
		}
		reqs := srv.seen()
		if len(reqs) != 1 {
			t.Fatalf("server saw %d requests, want 1", len(reqs))
		}
		assertRequest(t, reqs[0], 5, &modbus.ReadHoldingRegistersRequest{Address: 0x0030, Quantity: 3})
	})

	t.Run("ReadInputRegisters", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, nil)

		got, err := cli.ReadInputRegisters(ctx, 6, 0x0040, 2)
		if err != nil {
			t.Fatalf("ReadInputRegisters: %v", err)
		}
		if want := registerPattern(0x0040, 2, 2); !slices.Equal(got, want) {
			t.Fatalf("ReadInputRegisters = %v, want %v", got, want)
		}
		reqs := srv.seen()
		if len(reqs) != 1 {
			t.Fatalf("server saw %d requests, want 1", len(reqs))
		}
		assertRequest(t, reqs[0], 6, &modbus.ReadInputRegistersRequest{Address: 0x0040, Quantity: 2})
	})

	t.Run("WriteSingleCoil", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, nil)

		if err := cli.WriteSingleCoil(ctx, 7, 0x0011, true); err != nil {
			t.Fatalf("WriteSingleCoil: %v", err)
		}
		reqs := srv.seen()
		if len(reqs) != 1 {
			t.Fatalf("server saw %d requests, want 1", len(reqs))
		}
		assertRequest(t, reqs[0], 7, modbus.WriteSingleCoil{Address: 0x0011, Value: true})
	})

	t.Run("WriteSingleRegister", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, nil)

		if err := cli.WriteSingleRegister(ctx, 8, 0x0031, 0x1234); err != nil {
			t.Fatalf("WriteSingleRegister: %v", err)
		}
		reqs := srv.seen()
		if len(reqs) != 1 {
			t.Fatalf("server saw %d requests, want 1", len(reqs))
		}
		assertRequest(t, reqs[0], 8, modbus.WriteSingleRegister{Address: 0x0031, Value: 0x1234})
	})

	t.Run("WriteMultipleCoils", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, nil)

		values := []bool{true, false, true, true, false}
		if err := cli.WriteMultipleCoils(ctx, 9, 0x0012, values); err != nil {
			t.Fatalf("WriteMultipleCoils: %v", err)
		}
		reqs := srv.seen()
		if len(reqs) != 1 {
			t.Fatalf("server saw %d requests, want 1", len(reqs))
		}
		assertRequest(t, reqs[0], 9, &modbus.WriteMultipleCoilsRequest{Address: 0x0012, Values: values})
	})

	t.Run("WriteMultipleRegisters", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, nil)

		values := []uint16{0x0102, 0x0304, 0x0506}
		if err := cli.WriteMultipleRegisters(ctx, 10, 0x0032, values); err != nil {
			t.Fatalf("WriteMultipleRegisters: %v", err)
		}
		reqs := srv.seen()
		if len(reqs) != 1 {
			t.Fatalf("server saw %d requests, want 1", len(reqs))
		}
		assertRequest(t, reqs[0], 10, &modbus.WriteMultipleRegistersRequest{Address: 0x0032, Values: values})
	})
}

func TestExceptionIsADeliveredAnswer(t *testing.T) {
	srv := newFakeServer(t)
	srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
		return exceptionResponse(req, modbus.ExceptionIllegalDataAddress)
	})
	// Retries are configured to prove an exception response is never retried.
	cli := dialForTest(t, srv, func(cfg *Config) { cfg.Retries = 3 })
	ctx := context.Background()

	_, err := cli.ReadHoldingRegisters(ctx, 5, 0x0100, 2)
	if err == nil {
		t.Fatal("ReadHoldingRegisters: expected an exception error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a *gmodbus.Error", err)
	}
	if apiErr.Unit != 5 {
		t.Errorf("Error.Unit = %d, want 5", apiErr.Unit)
	}
	if apiErr.Function != modbus.FuncReadHoldingRegisters {
		t.Errorf("Error.Function = %#02x, want %#02x", apiErr.Function, modbus.FuncReadHoldingRegisters)
	}
	if apiErr.Code != modbus.ExceptionIllegalDataAddress {
		t.Errorf("Error.Code = %v, want %v", apiErr.Code, modbus.ExceptionIllegalDataAddress)
	}
	if !errors.Is(err, modbus.ErrException) {
		t.Errorf("errors.Is(err, modbus.ErrException) = false for %v", err)
	}
	if got := srv.count(); got != 1 {
		t.Fatalf("server saw %d requests, want exactly 1: an exception is not retried", got)
	}
	// The connection is still healthy: the exception was an answer.
	if _, err := cli.ReadHoldingRegisters(ctx, 5, 0x0100, 2); !errors.As(err, &apiErr) {
		t.Fatalf("second call after an exception: %v, want another *gmodbus.Error", err)
	}
}

func TestTimeoutThenSuccessOnRetry(t *testing.T) {
	srv := newFakeServer(t)
	srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
		if req.index == 0 {
			return nil // stall the first attempt
		}
		return cannedResponse(nil, req)
	})
	cli := dialForTest(t, srv, func(cfg *Config) {
		cfg.Timeout = 100 * time.Millisecond
		cfg.Retries = 2
	})

	got, err := cli.ReadHoldingRegisters(context.Background(), 1, 0x0020, 2)
	if err != nil {
		t.Fatalf("ReadHoldingRegisters: %v", err)
	}
	if want := registerPattern(0x0020, 2, 1); !slices.Equal(got, want) {
		t.Fatalf("ReadHoldingRegisters = %v, want %v", got, want)
	}
	reqs := srv.seen()
	if len(reqs) != 2 {
		t.Fatalf("server saw %d requests, want 2 (one timeout, one retry)", len(reqs))
	}
	if reqs[1].conn == reqs[0].conn {
		t.Errorf("the retry reused connection %d; a timed-out connection must not be reused", reqs[0].conn)
	}
	if reqs[0].tx != reqs[1].tx {
		t.Errorf("retry used transaction %d, want the same transaction %d", reqs[1].tx, reqs[0].tx)
	}
}

func TestRetriesExhaustedReturnsTimeout(t *testing.T) {
	srv := newFakeServer(t)
	srv.onRequest(func(*fakeServer, fakeRequest) *modbus.Frame { return nil })
	cli := dialForTest(t, srv, func(cfg *Config) {
		cfg.Timeout = 50 * time.Millisecond
		cfg.Retries = 2
	})
	ctx := context.Background()

	_, err := cli.ReadHoldingRegisters(ctx, 1, 0, 1)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("ReadHoldingRegisters: %v, want ErrTimeout", err)
	}
	if got := srv.count(); got != 3 {
		t.Fatalf("server saw %d requests, want 3 (one attempt plus two retries)", got)
	}

	// Every attempt failed, so the client has no connection left and says so
	// instead of looking healthy.
	_, err = cli.ReadHoldingRegisters(ctx, 1, 0, 1)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("call after exhausted retries: %v, want ErrClosed", err)
	}
	if got := srv.count(); got != 3 {
		t.Fatalf("a closed client sent a request: server saw %d, want 3", got)
	}
}

func TestTransportFailureThenRetry(t *testing.T) {
	srv := newFakeServer(t)
	srv.dropRequests(func(req fakeRequest) bool { return req.index == 0 })
	cli := dialForTest(t, srv, func(cfg *Config) { cfg.Retries = 1 })

	got, err := cli.ReadHoldingRegisters(context.Background(), 1, 0x0010, 2)
	if err != nil {
		t.Fatalf("ReadHoldingRegisters: %v", err)
	}
	if want := registerPattern(0x0010, 2, 1); !slices.Equal(got, want) {
		t.Fatalf("ReadHoldingRegisters = %v, want %v", got, want)
	}
	reqs := srv.seen()
	if len(reqs) != 2 {
		t.Fatalf("server saw %d requests, want 2 (the dropped one and its retry)", len(reqs))
	}
	if reqs[1].conn == reqs[0].conn {
		t.Errorf("the retry reused connection %d after a transport failure", reqs[0].conn)
	}
}

func TestTransportFailureClosesTheClient(t *testing.T) {
	srv := newFakeServer(t)
	srv.dropRequests(func(fakeRequest) bool { return true })
	cli := dialForTest(t, srv, nil) // one attempt: no retry
	ctx := context.Background()

	_, err := cli.ReadHoldingRegisters(ctx, 1, 0, 1)
	if err == nil {
		t.Fatal("ReadHoldingRegisters: expected a transport error")
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatalf("ReadHoldingRegisters = %v, want a transport error rather than a timeout", err)
	}
	// The broken connection is closed, so the client does not look healthy.
	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after a transport failure: %v, want ErrClosed", err)
	}
	if got := srv.count(); got != 1 {
		t.Fatalf("server saw %d requests, want 1", got)
	}
}

func TestResponseMustMatchTheRequest(t *testing.T) {
	ctx := context.Background()

	t.Run("transaction id", func(t *testing.T) {
		srv := newFakeServer(t)
		srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
			return replyWith(req, req.tx+1, req.unit)
		})
		cli := dialForTest(t, srv, func(cfg *Config) { cfg.Retries = 2 })

		_, err := cli.ReadHoldingRegisters(ctx, 1, 0, 1)
		if !errors.Is(err, ErrTransactionMismatch) {
			t.Fatalf("ReadHoldingRegisters: %v, want ErrTransactionMismatch", err)
		}
		if got := srv.count(); got != 1 {
			t.Fatalf("server saw %d requests, want 1: a mismatch is not retried", got)
		}
		if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 1); !errors.Is(err, ErrClosed) {
			t.Fatalf("call after a mismatch: %v, want ErrClosed", err)
		}
	})

	t.Run("unit id", func(t *testing.T) {
		srv := newFakeServer(t)
		srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
			return replyWith(req, req.tx, req.unit+1)
		})
		cli := dialForTest(t, srv, nil)

		_, err := cli.ReadCoils(ctx, 1, 0, 1)
		if !errors.Is(err, ErrTransactionMismatch) {
			t.Fatalf("ReadCoils: %v, want ErrTransactionMismatch", err)
		}
		if got := srv.count(); got != 1 {
			t.Fatalf("server saw %d requests, want 1", got)
		}
	})
}

func TestClosedClientFailsFast(t *testing.T) {
	srv := newFakeServer(t)
	cli := dialForTest(t, srv, nil)
	if cli.Addr() != srv.addr() {
		t.Errorf("Addr() = %q, want %q", cli.Addr(), srv.addr())
	}
	ctx := context.Background()

	if err := cli.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := cli.Close(); err != nil {
		t.Fatalf("second Close: %v, want nil", err)
	}
	if _, err := cli.ReadCoils(ctx, 1, 0, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadCoils after Close: %v, want ErrClosed", err)
	}
	if err := cli.WriteSingleRegister(ctx, 1, 0, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteSingleRegister after Close: %v, want ErrClosed", err)
	}
	if got := srv.count(); got != 0 {
		t.Fatalf("server saw %d requests from a closed client, want 0", got)
	}
}

func TestConcurrentCallersDoNotInterleave(t *testing.T) {
	srv := newFakeServer(t)
	srv.setProbePipelining(true)
	cli := dialForTest(t, srv, nil)

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			address := uint16(0x0100 + i*10)
			regs, err := cli.ReadHoldingRegisters(context.Background(), 1, address, 3)
			if err != nil {
				errs <- fmt.Errorf("caller %d: %w", i, err)
				return
			}
			if want := registerPattern(address, 3, 1); !slices.Equal(regs, want) {
				errs <- fmt.Errorf("caller %d at %#04x read %v, want %v", i, address, regs, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := srv.count(); got != callers {
		t.Fatalf("server saw %d requests, want %d", got, callers)
	}
	// The server reports a pipelining violation on cleanup, which fails the test.
}

func TestWaitingCallerHonorsItsContext(t *testing.T) {
	srv := newFakeServer(t)
	srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
		if req.index == 0 {
			// Hold the single exchange slot long enough for a second caller to
			// give up while waiting.
			time.Sleep(200 * time.Millisecond)
		}
		return cannedResponse(nil, req)
	})
	cli := dialForTest(t, srv, func(cfg *Config) { cfg.Timeout = 2 * time.Second })

	first := make(chan error, 1)
	go func() {
		_, err := cli.ReadHoldingRegisters(context.Background(), 1, 0x0010, 1)
		first <- err
	}()
	waitFor(t, 2*time.Second, func() bool { return srv.count() >= 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0x0011, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second caller: %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("second caller waited %s for a 20ms context", elapsed)
	}
	// The waiting caller gave up without disturbing the exchange in flight, and
	// without putting anything on the wire.
	if err := <-first; err != nil {
		t.Fatalf("first caller: %v", err)
	}
	if got := srv.count(); got != 1 {
		t.Fatalf("server saw %d requests, want 1: a caller that gave up while waiting must not send", got)
	}
}

func TestInvalidRequestIsRejectedLocally(t *testing.T) {
	srv := newFakeServer(t)
	cli := dialForTest(t, srv, nil)
	ctx := context.Background()

	checks := map[string]func() error{
		"read coils quantity 0": func() error {
			_, err := cli.ReadCoils(ctx, 1, 0, 0)
			return err
		},
		"read registers over the limit": func() error {
			_, err := cli.ReadHoldingRegisters(ctx, 1, 0, modbus.MaxReadRegisters+1)
			return err
		},
		"write multiple registers empty": func() error {
			return cli.WriteMultipleRegisters(ctx, 1, 0, nil)
		},
	}
	for name, check := range checks {
		if err := check(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	if got := srv.count(); got != 0 {
		t.Fatalf("server saw %d requests, want 0: an invalid request must not reach the wire", got)
	}
}

func TestDialValidatesConfig(t *testing.T) {
	cases := map[string]Config{
		"no address":        {},
		"negative timeout":  {Addr: "127.0.0.1:0", Timeout: -time.Second},
		"negative dial":     {Addr: "127.0.0.1:0", DialTimeout: -time.Second},
		"negative retries":  {Addr: "127.0.0.1:0", Retries: -1},
		"max pdu too large": {Addr: "127.0.0.1:0", MaxPDU: modbus.MaxPDU + 1},
		"negative reconnect delay": {
			Addr: "127.0.0.1:0", Reconnect: &ReconnectPolicy{Delay: -time.Second},
		},
		"negative reconnect max delay": {
			Addr: "127.0.0.1:0", Reconnect: &ReconnectPolicy{MaxDelay: -time.Second},
		},
		"negative reconnect attempts": {
			Addr: "127.0.0.1:0", Reconnect: &ReconnectPolicy{MaxAttempts: -1},
		},
		"reconnect max below delay": {
			Addr: "127.0.0.1:0", Reconnect: &ReconnectPolicy{Delay: 2 * time.Second, MaxDelay: time.Second},
		},
	}
	for name, cfg := range cases {
		if _, err := Dial(context.Background(), cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
