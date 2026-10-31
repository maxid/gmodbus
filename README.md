English | [简体中文](README.zh-CN.md)

# gmodbus

A **Modbus TCP master** for Go, built on the typed Modbus codec in
[gnety](https://github.com/maxid/gnety) (`codec/modbus`).

> Status: **development snapshot (untagged)** — `0.0.0-dev`. The wire format
> comes from the codec; this library owns the connection, the transaction, the
> retries, the exception mapping and the polling schedule.

## Features

- One TCP connection per client, typed operations for the function codes the
  platform uses: read coils, discrete inputs, holding registers and input
  registers; write single and multiple coils and registers.
- Automatic transaction ids and strict matching: a response whose transaction id
  or unit id does not match the request is rejected.
- `Timeout` bounds each attempt, `Retries` bounds timeout and transport retries,
  and a Modbus exception response is a delivered answer: it becomes a typed
  `*gmodbus.Error` and is never retried.
- Optional automatic reconnection: after a transport failure or a connection
  closed by the peer, the client redials with bounded, jittered exponential
  backoff and retries the in-flight request. Without the policy it keeps the
  fail-fast `ErrClosed` behavior.
- Pluggable transport: TLS with a handshake bounded by `DialTimeout`, or a
  custom `Dialer` for TLS, a proxy, a test double or any other `net.Conn`
  source.
- One request in flight per client. Concurrent callers are serialized on the
  wire and wait for the current exchange or for their own context.
- Low-cardinality metrics hook (function code and result only) with a nil-hook
  fast path.
- A polling runner that keeps independent tasks on their own schedule and
  reports their errors without stopping.

## Install

```sh
go get github.com/maxid/gmodbus@<commit>
```

No tags are published during development; use a commit pseudo-version.

## Quick start

```go
cli, err := gmodbus.Dial(ctx, gmodbus.Config{
    Addr:    "10.0.0.5:502",
    Timeout: time.Second,
    Retries: 2,
})
if err != nil {
    return err
}
defer cli.Close()

registers, err := cli.ReadHoldingRegisters(ctx, 1, 0x0000, 4)
if err != nil {
    return err
}
if err := cli.WriteSingleCoil(ctx, 1, 0x0010, true); err != nil {
    return err
}
```

## Examples

[`examples/read-holding`](examples/read-holding/main.go) reads holding registers
from the device given by `-addr` and prints them. There is no default host, so
without a reachable device the example exits non-zero with the transport error
instead of panicking:

```sh
go run ./examples/read-holding -addr 192.0.2.10:502
go run ./examples/read-holding -addr 127.0.0.1:5020 -unit 1 -start 0 -quantity 4
```

## Exceptions

A server that rejects a request answers with an exception response. The client
maps it to `*gmodbus.Error`, which carries the unit id, the request function
code and the exception code, and which unwraps to the codec's
`modbus.ErrException`:

```go
_, err := cli.ReadHoldingRegisters(ctx, 1, 0x9000, 1)
var rejected *gmodbus.Error
switch {
case errors.As(err, &rejected):
    // rejected.Code is modbus.ExceptionIllegalDataAddress here
case errors.Is(err, modbus.ErrException):
    // any exception code
}
```

## Polling

```go
tasks := []gmodbus.Task{
    {
        Name:  "temperature",
        Every: time.Second,
        Do: func(ctx context.Context) error {
            values, err := cli.ReadInputRegisters(ctx, 1, 0x0000, 1)
            if err != nil {
                return err
            }
            latest.Store(values[0])
            return nil
        },
    },
    {
        Name:  "setpoint",
        Every: 5 * time.Second,
        Do: func(ctx context.Context) error {
            return cli.WriteSingleRegister(ctx, 1, 0x0010, want.Load())
        },
    },
}

err := cli.Poll(ctx, tasks, func(task gmodbus.Task, err error) {
    log.Printf("poll %s: %v", task.Name, err)
})
```

`Poll` validates the tasks, runs each one immediately and then every `Every`
(on its own goroutine, never overlapping itself), reports failures through the
callback and returns only when the context ends. One failing task never stops
the loop or another task. Task names must be non-empty and unique, and `Every`
may not be shorter than `MinTaskInterval` (10ms).

A long-lived poller should set `Reconnect` so a lost connection is recovered in
place instead of every later task failing with `ErrClosed`:

```go
cli, err := gmodbus.Dial(ctx, gmodbus.Config{
    Addr: "10.0.0.5:502",
    Reconnect: &gmodbus.ReconnectPolicy{
        Delay:       100 * time.Millisecond,
        MaxDelay:    5 * time.Second,
        MaxAttempts: 5,
    },
})
```

## Behaviour

### Config

| Field | Meaning | Zero value |
| --- | --- | --- |
| `Addr` | server address, `host:port` | required |
| `DialTimeout` | bound on one TCP connect | `DefaultDialTimeout` (5s) |
| `Timeout` | bound on one attempt, write plus read | `DefaultTimeout` (5s) |
| `Retries` | extra attempts after the first, timeout/transport only | one attempt |
| `MaxPDU` | inbound PDU limit | `modbus.MaxPDU` |
| `Metrics` | per-attempt hook | none, and it costs nothing |
| `Reconnect` | opt-in recovery policy: delay, max delay and dial budget | nil, fail-fast |
| `TLS` | TLS config for the built-in dialer | plain TCP |
| `Dialer` | custom transport dialer, takes precedence over `TLS` | built-in TCP dialer |

Negative durations, a negative `Retries`, a negative reconnect field, a
reconnect `MaxDelay` below `Delay`, and a `MaxPDU` above `modbus.MaxPDU` are
rejected by `Dial`.

### Transactions and retries

Every operation takes the next transaction id from a per-client counter
(wraparound tolerated), writes the frame, and reads the reply through the codec
with the request it must answer. A reply with a different transaction id or unit
id is `ErrTransactionMismatch`.

An attempt either gets an answer or fails:

- **ok** — the response is decoded and returned.
- **exception** — a `*gmodbus.Error`; the connection stays usable and no retry
  happens.
- **timeout** — no complete response within `Timeout`.
- **transport** — the connection failed (refused, reset, closed, or a frame the
  codec rejected).
- **mismatch** — the reply does not answer this request.

A timeout or transport failure ends that attempt's connection, so a retry dials
a fresh one. Without `Reconnect`, when attempts run out, or when a reply is
unusable (mismatch or a frame the codec rejects), the client closes its
connection for good: every later call fails fast with `ErrClosed` instead of
looking healthy on a stream that may still carry a stale reply. Dial a new
client to recover.

### Reconnection

Set `Reconnect` to turn a transport failure or a connection closed by the peer
into an in-place recovery. The client drops the broken connection and dials
again with jittered exponential backoff: `Delay` for the first redial, doubling
per attempt and capped at `MaxDelay`. `MaxAttempts` bounds the dials one
operation may spend. Once a redial succeeds, the in-flight request is retried
with the same transaction id, so a poller survives a short outage without
seeing an error.

The whole sequence runs inside the caller's context and holds the single
exchange slot, so it adds no goroutine and no parallel dial. `Close` interrupts
a backoff wait or dial immediately. When the budget runs out the client reports
`ErrClosed` and stays closed; `Close` does the same explicitly. `Retries` still
bounds timeouts: a timeout does not start a reconnect, but with a `Reconnect`
policy a client that exhausts its timeout attempts remains usable for the next
call.

### TLS and custom dialers

`TLS` wraps the built-in TCP connection and completes the handshake within
`DialTimeout`. A failed handshake wraps `ErrTLSConfig` when retrying cannot help
(certificate verification, a peer that does not speak TLS, a TLS alert) and
`ErrTLSHandshake` when the failure is transient (timeout, reset, early EOF).
A reconnect spends no attempts on `ErrTLSConfig`.

`Dialer` takes precedence over `TLS`: when set, it owns the whole transport,
receives `"tcp"` and `Addr`, and may perform TLS itself with
`tls.DialWithDialer` or `tls.Client`. A `TLS` config alongside a custom dialer
is allowed and simply unused. `Dialer` must honor its context; `Close` cancels
it.

### Errors

| Error | Meaning |
| --- | --- |
| `ErrClosed` | the client has no connection: closed, or out of attempts |
| `ErrTimeout` | every attempt timed out |
| `ErrTransactionMismatch` | the reply did not answer the request |
| `ErrTLSConfig` | a permanent TLS handshake failure; a retry cannot fix it |
| `ErrTLSHandshake` | a transient TLS handshake failure; a reconnect may retry it |
| `*gmodbus.Error` | a Modbus exception response; unwraps to `modbus.ErrException` |

Sentinels are wrapped, so test them with `errors.Is`; recover the typed
exception with `errors.As`. Cancelling the caller's context aborts the pending
exchange with `ctx.Err()` and closes the connection, because the abandoned reply
would otherwise arrive during a later request.

### Concurrency

A client is safe for concurrent use, but it keeps one request in flight: every
call takes the same slot, and a second caller waits for the current exchange or
for its own context, whichever comes first. Modbus TCP has no out-of-order
guarantee here, so overlapping exchanges are deliberately impossible.

### Metrics

`Metrics.Request(function, result, duration)` runs once per attempt, on the
caller's goroutine. `result` is one of `ResultOK`, `ResultException`,
`ResultTimeout`, `ResultTransport` or `ResultMismatch`; a frame the codec rejects
is reported as `ResultTransport` because the connection is dropped. There are no
unit, address or value labels, by design.

## Scope

Modbus **TCP master** only. Out of scope, deliberately: serial RTU framing and
line settings, slave and server roles, register maps, device models and a
command line tool. The MBAP header and PDU encoding live in
`github.com/maxid/gnety/codec/modbus` and are never re-implemented here.

## Documentation

```sh
go doc -all .
```

## License

MIT. See [LICENSE](LICENSE).
