package gmodbus

import (
	"context"
	"errors"
	"sync"
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
