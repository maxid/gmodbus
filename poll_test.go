package gmodbus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not reached in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPollRunsTasksUntilCancelled(t *testing.T) {
	srv := newFakeServer(t)
	cli := dialForTest(t, srv, nil)

	var (
		mu                sync.Mutex
		reads             int
		readsAtFirstError int
		failures          int
		reported          []string
	)
	tasks := []Task{
		{
			Name:  "registers",
			Every: MinTaskInterval,
			Do: func(ctx context.Context) error {
				if _, err := cli.ReadHoldingRegisters(ctx, 1, 0, 2); err != nil {
					return err
				}
				mu.Lock()
				reads++
				mu.Unlock()
				return nil
			},
		},
		{
			Name:  "failing",
			Every: MinTaskInterval,
			Do: func(context.Context) error {
				mu.Lock()
				failures++
				if readsAtFirstError == 0 {
					readsAtFirstError = reads
				}
				mu.Unlock()
				return errors.New("device is unhappy")
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- cli.Poll(ctx, tasks, func(task Task, err error) {
			mu.Lock()
			reported = append(reported, task.Name)
			mu.Unlock()
		})
	}()

	// Both tasks must run repeatedly, and the healthy one must keep running
	// after the failing one reported its first error.
	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return failures >= 2 && reads >= 3 && reads > readsAtFirstError
	})

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Poll returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not return promptly after cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reported) == 0 {
		t.Fatal("onError was never called for the failing task")
	}
	for _, name := range reported {
		if name != "failing" {
			t.Errorf("onError reported task %q, want only the failing task", name)
		}
	}
	if reads < 3 {
		t.Errorf("the healthy task ran %d times, want at least 3", reads)
	}
}

func TestPollRejectsInvalidTasks(t *testing.T) {
	srv := newFakeServer(t)
	cli := dialForTest(t, srv, nil)
	ok := func(context.Context) error { return nil }

	cases := map[string][]Task{
		"no tasks": nil,
		"empty name": {
			{Every: MinTaskInterval, Do: ok},
		},
		"short interval": {
			{Name: "a", Every: MinTaskInterval - time.Millisecond, Do: ok},
		},
		"zero interval": {
			{Name: "a", Do: ok},
		},
		"nil do": {
			{Name: "a", Every: MinTaskInterval},
		},
		"negative jitter": {
			{Name: "a", Every: MinTaskInterval, Jitter: -time.Millisecond, Do: ok},
		},
		"jitter above interval": {
			{Name: "a", Every: MinTaskInterval, Jitter: 2 * MinTaskInterval, Do: ok},
		},
		"duplicate name": {
			{Name: "a", Every: MinTaskInterval, Do: ok},
			{Name: "a", Every: MinTaskInterval, Do: ok},
		},
	}
	for name, tasks := range cases {
		if err := cli.Poll(context.Background(), tasks, nil); err == nil {
			t.Errorf("%s: Poll accepted an invalid task list", name)
		}
	}
	if got := srv.count(); got != 0 {
		t.Fatalf("invalid poll tasks sent %d requests, want 0", got)
	}

	// A context that is already done returns before any task runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tasks := []Task{{
		Name:  "a",
		Every: MinTaskInterval,
		Do: func(context.Context) error {
			t.Error("a task ran although the context was already done")
			return nil
		},
	}}
	if err := cli.Poll(ctx, tasks, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Poll on a cancelled context = %v, want context.Canceled", err)
	}
}

func TestPollReportsFailuresWithoutStopping(t *testing.T) {
	srv := newFakeServer(t)
	cli := dialForTest(t, srv, nil)

	var (
		mu       sync.Mutex
		firstRun bool
		runs     int
	)
	// The callback takes the same lock the task uses: if Poll ever entered the
	// callback while a task still held it, the run counter would stop moving and
	// the wait below would time out.
	tasks := []Task{{
		Name:  "always-fails",
		Every: MinTaskInterval,
		Do: func(context.Context) error {
			mu.Lock()
			firstRun = true
			runs++
			mu.Unlock()
			return errors.New("boom")
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- cli.Poll(ctx, tasks, func(Task, error) {
			mu.Lock()
			defer mu.Unlock()
			time.Sleep(time.Millisecond)
		})
	}()
	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstRun && runs >= 3
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
}

// TestPollJitterDelaysOnlyTheFirstRun checks the point of Task.Jitter and the
// limit of it: the first run waits for an offset below the jitter, and the period
// afterwards is exactly Every. The jitter source is called once per task, which
// is what pins "the start is jittered, the schedule is not" without timing a
// hundred runs.
func TestPollJitterDelaysOnlyTheFirstRun(t *testing.T) {
	srv := newFakeServer(t)
	cli := dialForTest(t, srv, nil)
	var calls atomic.Int32
	cli.jitter = func(n int64) int64 {
		calls.Add(1)
		return n - 1 // the top of the range: the latest start the jitter allows
	}

	const (
		every  = 60 * time.Millisecond
		jitter = 40 * time.Millisecond
	)
	var (
		mu    sync.Mutex
		times []time.Time
	)
	task := Task{
		Name:   "spread",
		Every:  every,
		Jitter: jitter,
		Do: func(context.Context) error {
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
			return nil
		},
	}

	start := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cli.Poll(ctx, []Task{task}, nil) }()

	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(times) >= 3
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

	mu.Lock()
	defer mu.Unlock()
	// The lower bound is the assertion that matters: a task with a jitter must not
	// fire at t=0. The slack absorbs goroutine startup and timer granularity.
	if delay := times[0].Sub(start); delay < jitter-15*time.Millisecond {
		t.Fatalf("the first run happened after %s, want at least ~%s", delay, jitter)
	}
	// The upper bound keeps a broken implementation from passing by waiting far too
	// long; it is generous on purpose.
	if delay := times[0].Sub(start); delay > jitter+time.Second {
		t.Fatalf("the first run happened after %s, want at most ~%s", delay, jitter)
	}
	// Jitter is a start offset, never a drift: no two runs are closer than Every.
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < every-15*time.Millisecond {
			t.Fatalf("gap %d between runs is %s, want at least ~%s", i, gap, every)
		}
	}
	// One consultation per task: a jittered interval would call the source again
	// for every run.
	if got := calls.Load(); got != 1 {
		t.Fatalf("the jitter source was called %d times for %d runs, want once", got, len(times))
	}
}

// TestPollWithoutJitterStartsImmediately pins the zero value: no offset, and the
// jitter source is not consulted at all, so a task list without jitter costs
// nothing extra.
func TestPollWithoutJitterStartsImmediately(t *testing.T) {
	srv := newFakeServer(t)
	cli := dialForTest(t, srv, nil)
	var calls atomic.Int32
	cli.jitter = func(n int64) int64 {
		calls.Add(1)
		return n - 1
	}

	var runs atomic.Int32
	first := make(chan time.Time, 1)
	task := Task{
		Name:  "immediate",
		Every: time.Second,
		Do: func(context.Context) error {
			if runs.Add(1) == 1 {
				first <- time.Now()
			}
			return nil
		},
	}

	start := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cli.Poll(ctx, []Task{task}, nil) }()

	var firstRun time.Time
	select {
	case firstRun = <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("the task never ran")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not return after cancellation")
	}
	if delay := firstRun.Sub(start); delay > 500*time.Millisecond {
		t.Fatalf("the first run happened after %s, want immediately", delay)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("the jitter source was called %d times without a jitter", got)
	}
}

// TestPollJitterWaitIsCancellable checks that a task waiting out its start offset
// still ends when the poll context does: a jittered start must not turn a
// shutdown into a two-second wait.
func TestPollJitterWaitIsCancellable(t *testing.T) {
	srv := newFakeServer(t)
	cli := dialForTest(t, srv, nil)
	cli.jitter = func(n int64) int64 { return n - 1 }

	var ran atomic.Bool
	task := Task{
		Name:   "waits",
		Every:  2 * time.Second,
		Jitter: 2 * time.Second,
		Do: func(context.Context) error {
			ran.Store(true)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cli.Poll(ctx, []Task{task}, nil) }()
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Poll = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Poll waited out the jitter instead of returning on cancellation")
	}
	if ran.Load() {
		t.Fatal("the task ran although its start offset had not elapsed")
	}
}
