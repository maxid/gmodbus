package gmodbus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maxid/gnety/codec/modbus"
)

// metricEvent is one recorded Metrics.Request call.
type metricEvent struct {
	function byte
	result   string
	duration time.Duration
}

// metricRecorder is a Metrics implementation for tests.
type metricRecorder struct {
	mu     sync.Mutex
	events []metricEvent
}

// Request implements Metrics.
func (r *metricRecorder) Request(function byte, result string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, metricEvent{function: function, result: result, duration: d})
}

// snapshot returns the events recorded so far.
func (r *metricRecorder) snapshot() []metricEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]metricEvent, len(r.events))
	copy(out, r.events)
	return out
}

// assertEvents checks the result labels in order.
func assertEvents(t *testing.T, rec *metricRecorder, want ...string) {
	t.Helper()
	events := rec.snapshot()
	if len(events) != len(want) {
		t.Fatalf("recorded %d metrics %v, want %d %v", len(events), events, len(want), want)
	}
	for i, event := range events {
		if event.result != want[i] {
			t.Errorf("metric %d result = %q, want %q", i, event.result, want[i])
		}
		if event.function != modbus.FuncReadHoldingRegisters {
			t.Errorf("metric %d function = %#02x, want %#02x", i, event.function, modbus.FuncReadHoldingRegisters)
		}
		if event.duration < 0 {
			t.Errorf("metric %d duration = %s, want a non-negative duration", i, event.duration)
		}
	}
}

func TestMetricsRecordsOneEventPerAttempt(t *testing.T) {
	ctx := context.Background()

	t.Run("ok", func(t *testing.T) {
		srv := newFakeServer(t)
		rec := &metricRecorder{}
		cli := dialForTest(t, srv, func(cfg *ClientConfig) { cfg.Metrics = rec })

		if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 2); err != nil {
			t.Fatalf("ReadHoldingRegisters: %v", err)
		}
		assertEvents(t, rec, ResultOK)
	})

	t.Run("exception", func(t *testing.T) {
		srv := newFakeServer(t)
		srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
			return exceptionResponse(req, modbus.ExceptionIllegalDataAddress)
		})
		rec := &metricRecorder{}
		cli := dialForTest(t, srv, func(cfg *ClientConfig) {
			cfg.Metrics = rec
			cfg.Retries = 3
		})

		if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 2); err == nil {
			t.Fatal("ReadHoldingRegisters: expected an exception error")
		}
		assertEvents(t, rec, ResultException)
	})

	t.Run("timeout then ok", func(t *testing.T) {
		srv := newFakeServer(t)
		srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
			if req.index == 0 {
				return nil
			}
			return cannedResponse(nil, req)
		})
		rec := &metricRecorder{}
		cli := dialForTest(t, srv, func(cfg *ClientConfig) {
			cfg.Metrics = rec
			cfg.Timeout = 100 * time.Millisecond
			cfg.Retries = 1
		})

		if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 2); err != nil {
			t.Fatalf("ReadHoldingRegisters: %v", err)
		}
		assertEvents(t, rec, ResultTimeout, ResultOK)
	})

	t.Run("timeout exhausted", func(t *testing.T) {
		srv := newFakeServer(t)
		srv.onRequest(func(*fakeServer, fakeRequest) *modbus.Frame { return nil })
		rec := &metricRecorder{}
		cli := dialForTest(t, srv, func(cfg *ClientConfig) {
			cfg.Metrics = rec
			cfg.Timeout = 50 * time.Millisecond
			cfg.Retries = 1
		})

		if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 2); err == nil {
			t.Fatal("ReadHoldingRegisters: expected ErrTimeout")
		}
		assertEvents(t, rec, ResultTimeout, ResultTimeout)
	})

	t.Run("mismatch", func(t *testing.T) {
		srv := newFakeServer(t)
		srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
			return replyWith(req, req.tx+1, req.unit)
		})
		rec := &metricRecorder{}
		cli := dialForTest(t, srv, func(cfg *ClientConfig) { cfg.Metrics = rec })

		if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 2); err == nil {
			t.Fatal("ReadHoldingRegisters: expected ErrResponseMismatch")
		}
		assertEvents(t, rec, ResultMismatch)
	})

	t.Run("nil hook", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, func(cfg *ClientConfig) { cfg.Metrics = nil })

		if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 2); err != nil {
			t.Fatalf("ReadHoldingRegisters with a nil hook: %v", err)
		}
	})

	t.Run("noop hook", func(t *testing.T) {
		srv := newFakeServer(t)
		cli := dialForTest(t, srv, func(cfg *ClientConfig) { cfg.Metrics = NoopMetrics{} })

		if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 2); err != nil {
			t.Fatalf("ReadHoldingRegisters with NoopMetrics: %v", err)
		}
	})
}
