package gmodbus

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxid/gnety/codec/modbus"
)

func TestPeerDropWithoutReconnectFailsFast(t *testing.T) {
	srv := newFakeServer(t)
	srv.dropRequests(func(req fakeRequest) bool { return req.index == 1 })
	cli := dialForTest(t, srv, nil)
	ctx := context.Background()

	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0x0010, 2); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// The peer closes the connection instead of answering the second request.
	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0x0010, 2); err == nil {
		t.Fatal("second read: expected a transport error")
	} else if errors.Is(err, ErrTimeout) {
		t.Fatalf("second read = %v, want a transport error", err)
	}
	// Without a reconnect policy the failed connection is gone for good.
	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0x0010, 2); !errors.Is(err, ErrClosed) {
		t.Fatalf("third read = %v, want ErrClosed", err)
	}
	if got := srv.count(); got != 2 {
		t.Fatalf("server saw %d requests, want 2: a closed client must not send", got)
	}
	if got := srv.connectionCount(); got != 1 {
		t.Fatalf("server accepted %d connections, want 1", got)
	}
}

func TestPeerDropWithReconnectRetriesTheRequest(t *testing.T) {
	srv := newFakeServer(t)
	srv.dropRequests(func(req fakeRequest) bool { return req.index == 1 })
	cli := dialForTest(t, srv, func(cfg *ClientConfig) {
		// No ordinary retries: recovery comes from the reconnect policy alone.
		cfg.Retries = 0
		cfg.Reconnect = &ReconnectPolicy{
			Delay:       time.Millisecond,
			MaxDelay:    2 * time.Millisecond,
			MaxAttempts: 3,
		}
	})
	ctx := context.Background()

	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0x0010, 2); err != nil {
		t.Fatalf("first read: %v", err)
	}
	got, err := cli.ReadHoldingRegisters(ctx, 1, 0x0010, 2)
	if err != nil {
		t.Fatalf("read across a dropped connection: %v", err)
	}
	if want := registerPattern(0x0010, 2, 1); !slices.Equal(got, want) {
		t.Fatalf("read across reconnect = %v, want %v", got, want)
	}
	reqs := srv.seen()
	if len(reqs) != 3 {
		t.Fatalf("server saw %d requests, want 3 (one answered, one dropped, one retried)", len(reqs))
	}
	if reqs[0].conn != 1 || reqs[1].conn != 1 || reqs[2].conn != 2 {
		t.Fatalf("requests arrived on connections %d, %d, %d; want 1, 1, 2",
			reqs[0].conn, reqs[1].conn, reqs[2].conn)
	}
	if reqs[1].tx != reqs[2].tx {
		t.Errorf("retry used transaction %d, want the same transaction %d", reqs[2].tx, reqs[1].tx)
	}

	// The replacement connection stays live for later calls.
	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0x0010, 2); err != nil {
		t.Fatalf("read after reconnect: %v", err)
	}
	if got := srv.connectionCount(); got != 2 {
		t.Fatalf("server accepted %d connections, want 2", got)
	}
}

func TestPollSurvivesReconnect(t *testing.T) {
	srv := newFakeServer(t)
	// The first connection serves one request and is then dropped; every later
	// request runs on the replacement connection.
	srv.dropRequests(func(req fakeRequest) bool { return req.conn == 1 && req.index >= 1 })
	cli := dialForTest(t, srv, func(cfg *ClientConfig) {
		cfg.Reconnect = &ReconnectPolicy{
			Delay:       time.Millisecond,
			MaxDelay:    2 * time.Millisecond,
			MaxAttempts: 3,
		}
	})

	var (
		reads    atomic.Int64
		reported atomic.Int64
	)
	task := Task{
		Name:  "registers",
		Every: MinTaskInterval,
		Do: func(ctx context.Context) error {
			if _, err := cli.ReadHoldingRegisters(ctx, 1, 0x0020, 2); err != nil {
				return err
			}
			reads.Add(1)
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- cli.Poll(ctx, []Task{task}, func(Task, error) { reported.Add(1) })
	}()

	waitFor(t, 5*time.Second, func() bool {
		return reads.Load() >= 4 && srv.connectionCount() >= 2
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Poll = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not return after cancellation")
	}

	if reported.Load() != 0 {
		t.Errorf("poller reported %d errors; the dropped request should have been retried", reported.Load())
	}
	reqs := srv.seen()
	if len(reqs) < 5 {
		t.Fatalf("server saw %d requests, want at least 5", len(reqs))
	}
	if reqs[0].conn != 1 {
		t.Errorf("first request used connection %d, want 1", reqs[0].conn)
	}
	if reqs[len(reqs)-1].conn < 2 {
		t.Errorf("last request used connection %d, want a reconnected connection", reqs[len(reqs)-1].conn)
	}
}

func TestTimeoutExhaustionKeepsClientUsableWithReconnect(t *testing.T) {
	srv := newFakeServer(t)
	srv.onRequest(func(_ *fakeServer, req fakeRequest) *modbus.Frame {
		if req.index == 0 {
			return nil // the first attempt times out
		}
		return cannedResponse(nil, req)
	})
	cli := dialForTest(t, srv, func(cfg *ClientConfig) {
		cfg.Timeout = 30 * time.Millisecond
		cfg.Retries = 0
		cfg.Reconnect = &ReconnectPolicy{Delay: time.Millisecond, MaxDelay: time.Millisecond, MaxAttempts: 1}
	})
	ctx := context.Background()

	if _, err := cli.ReadHoldingRegisters(ctx, 1, 0x0020, 2); !errors.Is(err, ErrTimeout) {
		t.Fatalf("first read = %v, want ErrTimeout", err)
	}
	// The timeout dropped the connection, but the reconnect policy keeps the
	// client usable instead of turning later calls into ErrClosed.
	got, err := cli.ReadHoldingRegisters(ctx, 1, 0x0020, 2)
	if err != nil {
		t.Fatalf("read after an exhausted timeout: %v", err)
	}
	if want := registerPattern(0x0020, 2, 1); !slices.Equal(got, want) {
		t.Fatalf("read after an exhausted timeout = %v, want %v", got, want)
	}
	if got := srv.connectionCount(); got != 2 {
		t.Fatalf("server accepted %d connections, want 2", got)
	}
}

func TestReconnectBackoffIsBoundedAndJittered(t *testing.T) {
	policy := &ReconnectPolicy{
		Delay:       10 * time.Millisecond,
		MaxDelay:    80 * time.Millisecond,
		MaxAttempts: 10,
	}
	cases := []struct {
		attempt int
		base    time.Duration
	}{
		{1, 10 * time.Millisecond},
		{2, 20 * time.Millisecond},
		{3, 40 * time.Millisecond},
		{4, 80 * time.Millisecond},
		{5, 80 * time.Millisecond},
		{50, 80 * time.Millisecond},
	}
	for _, tc := range cases {
		min := policy.delay(tc.attempt, func(int64) int64 { return 0 })
		max := policy.delay(tc.attempt, func(n int64) int64 { return n - 1 })
		if min != tc.base/2 {
			t.Errorf("attempt %d minimum delay = %s, want %s", tc.attempt, min, tc.base/2)
		}
		if max != tc.base-1 {
			t.Errorf("attempt %d maximum delay = %s, want %s", tc.attempt, max, tc.base-1)
		}
		if min >= max {
			t.Errorf("attempt %d is not jittered: min %s, max %s", tc.attempt, min, max)
		}
		if max > policy.MaxDelay {
			t.Errorf("attempt %d delay %s exceeds cap %s", tc.attempt, max, policy.MaxDelay)
		}
	}
}

func TestReconnectGivesUpAfterBoundedAttempts(t *testing.T) {
	srv := newFakeServer(t)
	srv.dropRequests(func(fakeRequest) bool { return true })
	cli := dialForTest(t, srv, func(cfg *ClientConfig) {
		cfg.Retries = 0
		cfg.Reconnect = &ReconnectPolicy{
			Delay:       2 * time.Millisecond,
			MaxDelay:    4 * time.Millisecond,
			MaxAttempts: 2,
		}
	})

	start := time.Now()
	_, err := cli.ReadHoldingRegisters(context.Background(), 1, 0, 1)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("read = %v, want ErrClosed after the reconnect budget ran out", err)
	}
	// One initial request plus one retry per redial: the budget is two redials.
	if got := srv.count(); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	if got := srv.connectionCount(); got != 3 {
		t.Fatalf("server accepted %d connections, want 3", got)
	}
	// Two jittered waits of 1..2ms and 2..4ms happened before the redials.
	if elapsed < 2*time.Millisecond {
		t.Errorf("reconnect gave up after %s; the backoff waits did not run", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("reconnect took %s, want bounded waits", elapsed)
	}

	// The give-up is terminal: no more dials or requests.
	if _, err := cli.ReadHoldingRegisters(context.Background(), 1, 0, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after reconnect give-up = %v, want ErrClosed", err)
	}
	if got := srv.count(); got != 3 {
		t.Fatalf("server saw %d requests after give-up, want 3", got)
	}
}

func TestCloseInterruptsReconnectWait(t *testing.T) {
	srv := newFakeServer(t)
	srv.dropRequests(func(fakeRequest) bool { return true })
	cli := dialForTest(t, srv, func(cfg *ClientConfig) {
		cfg.Reconnect = &ReconnectPolicy{
			Delay:       time.Second,
			MaxDelay:    time.Second,
			MaxAttempts: 3,
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := cli.ReadHoldingRegisters(context.Background(), 1, 0, 1)
		done <- err
	}()
	waitFor(t, 2*time.Second, func() bool { return srv.count() >= 1 })
	// Give the client time to enter the one-second backoff wait.
	time.Sleep(20 * time.Millisecond)

	closed := time.Now()
	if err := cli.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("read during reconnect = %v, want ErrClosed", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close did not interrupt the reconnect wait")
	}
	if elapsed := time.Since(closed); elapsed > 250*time.Millisecond {
		t.Errorf("Close took %s to stop the reconnect wait", elapsed)
	}
	if got := srv.count(); got != 1 {
		t.Fatalf("server saw %d requests after Close, want 1", got)
	}
	if got := srv.connectionCount(); got != 1 {
		t.Fatalf("server accepted %d connections after Close, want 1", got)
	}
}

func TestReconnectWaitHonorsTheCallerContext(t *testing.T) {
	srv := newFakeServer(t)
	srv.dropRequests(func(fakeRequest) bool { return true })
	cli := dialForTest(t, srv, func(cfg *ClientConfig) {
		cfg.Reconnect = &ReconnectPolicy{
			Delay:       time.Second,
			MaxDelay:    time.Second,
			MaxAttempts: 3,
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := cli.ReadHoldingRegisters(ctx, 1, 0, 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("read waited %s for a 20ms context", elapsed)
	}
	if got := srv.connectionCount(); got != 1 {
		t.Fatalf("server accepted %d connections, want only the initial one", got)
	}
}
