package gmodbus

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// Defaults used when a ReconnectPolicy field is zero.
const (
	// DefaultReconnectDelay is the first delay before a redial.
	DefaultReconnectDelay = 100 * time.Millisecond
	// DefaultReconnectMaxDelay caps the exponential backoff.
	DefaultReconnectMaxDelay = 5 * time.Second
	// DefaultReconnectMaxAttempts bounds the dials one operation may make to
	// recover a lost connection.
	DefaultReconnectMaxAttempts = 5
)

// ReconnectPolicy is an opt-in automatic reconnection policy.
//
// A nil pointer in Config keeps the fail-fast behavior: a transport failure
// ends the operation and closes the client. A non-nil policy lets a client
// recover from a connection the peer closed or a transport failure: it drops
// the broken connection, waits with jittered exponential backoff and dials
// again. The in-flight request is then retried, so a poller keeps polling
// across a short outage instead of seeing ErrClosed.
//
// Backoff starts at Delay, doubles per attempt and never exceeds MaxDelay.
// Every wait is jittered to roughly half..full of the current delay, so
// several clients recovering from the same outage do not dial in lockstep.
// MaxAttempts bounds the dials one operation may spend; the caller's context
// and Client.Close both interrupt a wait immediately.
type ReconnectPolicy struct {
	// Delay is the delay before the first redial. Zero means
	// DefaultReconnectDelay.
	Delay time.Duration
	// MaxDelay caps the exponential backoff. Zero means
	// DefaultReconnectMaxDelay, raised to Delay when Delay is larger.
	MaxDelay time.Duration
	// MaxAttempts bounds the dials one operation may make to recover a lost
	// connection. Zero means DefaultReconnectMaxAttempts.
	MaxAttempts int
}

// normalizeReconnect validates a policy and fills its defaults.
func normalizeReconnect(p *ReconnectPolicy) (*ReconnectPolicy, error) {
	if p == nil {
		return nil, nil
	}
	out := *p
	if out.Delay < 0 {
		return nil, errors.New("gmodbus: Config.Reconnect.Delay is negative")
	}
	if out.MaxDelay < 0 {
		return nil, errors.New("gmodbus: Config.Reconnect.MaxDelay is negative")
	}
	if out.MaxAttempts < 0 {
		return nil, errors.New("gmodbus: Config.Reconnect.MaxAttempts is negative")
	}
	if out.Delay == 0 {
		out.Delay = DefaultReconnectDelay
	}
	if out.MaxDelay == 0 {
		out.MaxDelay = DefaultReconnectMaxDelay
		if out.Delay > out.MaxDelay {
			out.MaxDelay = out.Delay
		}
	}
	if out.MaxDelay < out.Delay {
		return nil, fmt.Errorf("gmodbus: Config.Reconnect.MaxDelay %s is below Delay %s", out.MaxDelay, out.Delay)
	}
	if out.MaxAttempts == 0 {
		out.MaxAttempts = DefaultReconnectMaxAttempts
	}
	return &out, nil
}

// delay is the jittered wait before the given one-based reconnect attempt.
// jitter is injectable so tests can pin the randomness; production passes
// rand.Int64N.
func (p *ReconnectPolicy) delay(attempt int, jitter func(int64) int64) time.Duration {
	base := p.Delay
	for i := 1; i < attempt && base < p.MaxDelay; i++ {
		if base > p.MaxDelay/2 {
			base = p.MaxDelay
			break
		}
		base *= 2
	}
	if base > p.MaxDelay {
		base = p.MaxDelay
	}
	half := base / 2
	if half <= 0 {
		return base
	}
	n := jitter(int64(half))
	if n < 0 {
		n = 0
	}
	if n >= int64(half) {
		n = int64(half) - 1
	}
	return half + time.Duration(n)
}

// reconnect re-dials after a transport failure, spending at most *budget dial
// attempts and waiting with bounded, jittered backoff before each. It returns
// nil once a new connection is installed. A permanent TLS configuration error
// ends the sequence immediately; otherwise the last dial error is returned when
// the budget runs out.
func (c *Client) reconnect(ctx context.Context, budget *int, last error) error {
	policy := c.reconnectPolicy
	for *budget > 0 {
		if c.isClosed() {
			return ErrClosed
		}
		attempt := policy.MaxAttempts - *budget + 1
		if err := c.wait(ctx, policy.delay(attempt, c.jitter)); err != nil {
			return err
		}
		if c.isClosed() {
			return ErrClosed
		}
		*budget--

		conn, err := c.dial(ctx)
		if err != nil {
			if isTLSConfigError(err) {
				return err
			}
			last = err
			continue
		}
		if _, err := c.install(conn); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("gmodbus: reconnect gave up after %d attempt(s): %w", policy.MaxAttempts, last)
}

// wait sleeps for d or returns early when the caller's context ends or the
// client is closed. It owns no goroutine, so a Close cannot leave a timer
// running behind.
func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closeCtx.Done():
			return ErrClosed
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closeCtx.Done():
		return ErrClosed
	}
}

// jitterInt64N is the production jitter source.
func jitterInt64N(n int64) int64 { return rand.Int64N(n) }
