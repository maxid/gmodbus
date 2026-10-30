package gmodbus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// MinTaskInterval is the shortest schedule Poll accepts. Anything faster is a
// busy loop on a single connection, not a poll.
const MinTaskInterval = 10 * time.Millisecond

// Task is one scheduled operation: a named callback run on its own period.
type Task struct {
	// Name identifies the task in errors and in the onError callback. It must be
	// non-empty and unique among the tasks of one Poll call.
	Name string
	// Every is the period between two runs of Do. It must be at least
	// MinTaskInterval.
	Every time.Duration
	// Do performs the work. It receives the Poll context and must return when
	// that context ends; Poll waits for an in-flight Do to return.
	Do func(context.Context) error
}

// Poll runs every task on its own schedule until ctx is done.
//
// Each task starts immediately and then runs again Every after its previous run
// returned, so a slow Do never overlaps itself. Tasks run concurrently; the
// client serializes their requests on the wire like any other caller.
//
// A task that returns an error is reported through onError and then scheduled
// again: one failing task never stops the loop or the other tasks. onError is
// called from the task goroutines but serialized, so it is never entered twice
// at once; a nil onError discards errors. Errors produced because ctx ended are
// not reported.
//
// Poll returns only when ctx is done, after the in-flight Do calls returned, and
// reports ctx.Err(). Individual task failures are never returned. An invalid
// task list is rejected before any goroutine starts.
func (c *Client) Poll(ctx context.Context, tasks []Task, onError func(Task, error)) error {
	if err := validateTasks(tasks); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var (
		wg     sync.WaitGroup
		errMu  sync.Mutex
		report = func(task Task, err error) {
			if onError == nil {
				return
			}
			errMu.Lock()
			defer errMu.Unlock()
			onError(task, err)
		}
	)
	for _, task := range tasks {
		wg.Add(1)
		go func(task Task) {
			defer wg.Done()
			runTask(ctx, task, report)
		}(task)
	}
	wg.Wait()
	return ctx.Err()
}

// runTask runs one task on its own timer until ctx ends.
func runTask(ctx context.Context, task Task, report func(Task, error)) {
	// The first run happens immediately: a poller should not wait a full period
	// before the first read of a device.
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := task.Do(ctx); err != nil && ctx.Err() == nil {
			report(task, err)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(task.Every)
	}
}

// validateTasks enforces the Task contract before Poll starts anything.
func validateTasks(tasks []Task) error {
	if len(tasks) == 0 {
		return errors.New("gmodbus: poll needs at least one task")
	}
	seen := make(map[string]struct{}, len(tasks))
	for i, task := range tasks {
		switch {
		case task.Name == "":
			return fmt.Errorf("gmodbus: poll task %d has an empty name", i)
		case task.Every < MinTaskInterval:
			return fmt.Errorf("gmodbus: poll task %q interval %s is below the %s minimum",
				task.Name, task.Every, MinTaskInterval)
		case task.Do == nil:
			return fmt.Errorf("gmodbus: poll task %q has no Do func", task.Name)
		}
		if _, ok := seen[task.Name]; ok {
			return fmt.Errorf("gmodbus: poll task name %q is used twice", task.Name)
		}
		seen[task.Name] = struct{}{}
	}
	return nil
}
