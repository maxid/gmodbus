English | [简体中文](README.zh-CN.md)

# gmodbus

A **Modbus client and server** for Go, built on the typed Modbus codec in
[gnety](https://github.com/maxid/gnety) (`codec/modbus`).

> Status: **development snapshot (untagged)** — `0.0.0-dev`. The wire format
> comes from the codec; this library owns the connection, the transaction, the
> retries, the exception mapping, the polling schedule and the server dispatch.

## Features

- **Seven transports**: TCP, TLS over TCP, UDP, DTLS, RTU over a serial line, RTU
  over TCP and RTU over UDP — for both the client and the server.
- **Typed client** for function codes 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x0F,
  0x10, 0x16, 0x17 and 0x2B/0x0E.
- **Server with a built-in register map**: coils, discrete inputs, holding
  registers and input registers, kept independently per unit id. A `Handler` can
  take over any request.
- **Vendor function codes**: a typed registry decodes a non-standard function
  code, or replaces a standard code's payload, without changing the library.
- Automatic transaction ids and strict matching; a response that does not answer
  the request is rejected.
- `Timeout` bounds each attempt, `Retries` bounds timeout and transport retries,
  and a Modbus exception response is a delivered answer: it becomes a typed
  `*gmodbus.Error` and is never retried.
- Optional automatic reconnection with bounded, jittered exponential backoff.
- RTU CRC16 by default; RTU over TCP/UDP can omit it for gateways that strip the
  checksum. A serial line always keeps it.
- RTU broadcast (unit 0): a write with no reply; a read is refused.
- Low-cardinality client and server metrics hooks with nil-hook fast paths.
- A polling runner that keeps independent tasks on their own schedule.

## Install

```sh
go get github.com/maxid/gmodbus@<commit>
```

No tags are published during development; use a commit pseudo-version.

## Quick start

```go
cli, err := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
    Addr:    "10.0.0.5:502", // NetworkTCP is the zero value
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

A server is one call:

```go
srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
    Addr: "127.0.0.1:5020",
    Device: &gmodbus.DeviceIdentification{VendorName: "ACME", ProductCode: "PLC-1"},
})
if err != nil {
    return err
}
defer srv.Close()
```

`NewServer` starts serving immediately; `Addr()` returns the bound address (or
the serial path), so a test can use `127.0.0.1:0`.

## Transports

`Network` selects the transport; the zero value is `NetworkTCP`.

| Network | Constant | Framing | Client options | Server options |
| --- | --- | --- | --- | --- |
| `tcp` | `NetworkTCP` | MBAP over one TCP connection | `Addr` | `Addr`, `Listener` |
| `tls` | `NetworkTLS` | MBAP over TLS | `TLS` (optional) | `TLS` (required unless `Listener` is a TLS listener) |
| `udp` | `NetworkUDP` | one MBAP frame per datagram | `Addr` | `Addr`, `PacketConn` |
| `dtls` | `NetworkDTLS` | one MBAP frame per DTLS record | `DTLS` (required) | `DTLS` (required unless `Listener` is a DTLS listener) |
| `rtu` | `NetworkRTU` | CRC16-framed RTU over a serial line; `Addr` is the device path | `Addr` or `Port`, `Serial` | `Addr` or `Port`, `Serial` |
| `rtu-tcp` | `NetworkRTUOverTCP` | CRC16-framed RTU over TCP | `Addr`, `RTU` | `Addr`, `Listener`, `RTU` |
| `rtu-udp` | `NetworkRTUOverUDP` | CRC16-framed RTU, one frame per datagram | `Addr`, `RTU` | `Addr`, `PacketConn`, `RTU` |

`ClientConfig.TLS` defaults to a configuration that trusts the system roots and
takes `ServerName` from `Addr`. `ClientConfig.DTLS` is required and its
`ServerName` is filled from `Addr` when empty. Both server fields are used as
given; the caller owns the configuration.

`RTU` controls the RTU framing variant. The zero value appends and verifies the
CRC; `modbus.RTUOptions{NoCRC: true}` omits it on `rtu-tcp`/`rtu-udp`. `NoCRC` is
rejected on the serial transport, where the specification requires the checksum.

## Client

### Config

| Field | Meaning | Zero value |
| --- | --- | --- |
| `Network` | transport | `NetworkTCP` |
| `Addr` | `host:port`, or the serial device path | required (unless `Port`) |
| `DialTimeout` | bound on one connect and the TLS/DTLS handshake | `DefaultDialTimeout` (5s) |
| `Timeout` | bound on one attempt, write plus read | `DefaultTimeout` (5s) |
| `Retries` | extra attempts after the first, timeout/transport only | one attempt |
| `MaxPDU` | inbound PDU limit | `modbus.MaxPDU` |
| `Metrics` | per-attempt hook | none, and it costs nothing |
| `Reconnect` | opt-in recovery policy | nil, fail-fast |
| `TLS` | TLS config for `tls` | system roots |
| `DTLS` | DTLS config for `dtls` | required for `dtls` |
| `Serial` | line parameters for `rtu` | 19200, 8 data bits, 1 stop bit, no parity |
| `RTU` | RTU framing variant for `rtu-tcp`/`rtu-udp` | CRC on |
| `Functions` | vendor function-code registry | built-in codes only |
| `Port` | already-open serial line for `rtu` | open `Addr` with `Serial` |

`SerialConfig` carries `Baud`, `DataBits`, `StopBits` and `Parity`; a zero field
takes the default above. A supplied `Port` owns its own line settings and must
honor `SetDeadline`; it is closed by `Client.Close`.

### Operations

| Function | Method | Result |
| --- | --- | --- |
| 0x01 Read Coils | `ReadCoils(ctx, unit, addr, qty)` | `[]bool` |
| 0x02 Read Discrete Inputs | `ReadDiscreteInputs` | `[]bool` |
| 0x03 Read Holding Registers | `ReadHoldingRegisters` | `[]uint16` |
| 0x04 Read Input Registers | `ReadInputRegisters` | `[]uint16` |
| 0x05 Write Single Coil | `WriteSingleCoil` | error |
| 0x06 Write Single Register | `WriteSingleRegister` | error |
| 0x0F Write Multiple Coils | `WriteMultipleCoils` | error |
| 0x10 Write Multiple Registers | `WriteMultipleRegisters` | error |
| 0x16 Mask Write Register | `MaskWriteRegister(ctx, unit, addr, and, or)` | error |
| 0x17 Read/Write Multiple Registers | `ReadWriteMultipleRegisters(ctx, unit, readAddr, readQty, writeAddr, values)` | `[]uint16` |
| 0x2B/0x0E Read Device Identification | `ReadDeviceIdentification(ctx, unit, readCode, objectID)` | `*modbus.ReadDeviceIdentificationResponse` |
| any registered message | `Execute(ctx, unit, req)` | `modbus.Message` |

A read/write-multiple request applies the write before the read and is one
transaction. A device-identification response may set `MoreFollows` and
`NextObjectID`; send another request with the same read code and that object id
to continue.

### Vendor function codes

`ClientConfig.Functions` and `ServerConfig.Functions` share one registry from
`gnety/codec/modbus`:

```go
functions := modbus.Functions{
    0x64: {
        DecodeRequest:  func(pdu []byte) (modbus.Message, error) { ... },
        DecodeResponse: func(req modbus.Message, pdu []byte) (modbus.Message, error) { ... },
        // Only needed on RTU serial or RTU-over-TCP, which have no length field.
        RequestLength:  func(frame []byte) (int, error) { ... },
        ResponseLength: func(req modbus.Message, frame []byte) (int, error) { ... },
    },
}

cli, _ := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
    Addr:      "10.0.0.5:502",
    Functions: functions,
})
resp, err := cli.Execute(ctx, 1, &myVendorRequest{...})
```

The registry is consulted before the built-in decoder, so it can also replace
the payload of a standard function code. An exception response is always decoded
by the built-in decoder, so a vendor request still receives one correctly.

### RTU broadcast

On `rtu`, `rtu-tcp` and `rtu-udp` unit 0 is a broadcast. The write methods that
change state (0x05, 0x06, 0x0F, 0x10, 0x16) send the request and return once it
is written; the server applies it without a reply. A read, and 0x17 because it
carries a read, returns an error instead of going on the wire. On the MBAP
transports unit 0 is an ordinary unit id.

A broadcast has no acknowledgement, so a retry or a reconnect after a transport
failure can apply the same write twice; a broadcast write is only safe where the
device treats it as idempotent.

## Server

```go
store := gmodbus.NewMemoryStore(nil) // unit 1, 1024 points per bank
unit := store.AddUnit(7, gmodbus.UnitConfig{HoldingRegisters: 256})

srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
    Network: gmodbus.NetworkTCP,
    Addr:    ":502",
    Store:   store,
    Handler: gmodbus.HandlerFunc(func(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
        return nil, gmodbus.ErrUnhandled // let the store answer
    }),
    Device: &gmodbus.DeviceIdentification{
        VendorName:         "ACME",
        ProductCode:        "PLC-1",
        MajorMinorRevision: "1.2",
    },
})
```

The server binds the transport and starts serving in `NewServer`. `Close` stops
listening, closes every connection and the serial port; cancelling the context
passed to `NewServer` does the same.

### Dispatch

1. `Handler` runs first. A returned response is sent; a `*modbus.Exception`
   (as an error or as a message) is sent as that exception; `ErrUnhandled` falls
   through to the store. Any other error is reported through `OnError` and
   becomes `ServerDeviceFailure`.
2. `MemoryStore` answers the standard codes. An address outside a bank becomes
   `IllegalDataAddress`; an unknown function code returns `ErrUnhandled`.
3. `DeviceIdentification` answers 0x2B/0x0E when the handler and store leave it
   unhandled.
4. Anything still unhandled becomes `IllegalFunction`.

A panic in a handler is recovered, reported through `OnError`, answered with
`ServerDeviceFailure` and counted as `ServerResultError`. The context passed to a
handler is cancelled when the request finishes or the server closes, so a
blocking handler releases `Close`.

### MemoryStore

- One independent set of four banks per unit id; every bank is addressed from 0.
- `AddUnit` creates or replaces a unit. Replacing a unit a request is using
  discards that request's writes to the old banks.
- `UnitConfig` sizes the banks; a zero field takes `DefaultUnitSize` (1024). A
  request for a unit that does not exist yet creates it with the store's default
  configuration, so any unit id works.
- `UnitStore` exposes `Coils`, `SetCoils`, `Coil`, `SetCoil`, `HoldingRegisters`,
  `SetHoldingRegisters`, `MaskWrite`, `ReadWrite` and the equivalent methods for
  discrete inputs and input registers.
- `OnWrite` reports successful writes with copies of the changed values; it runs
  outside the store lock, so it must not block. A broadcast reports one event per
  unit.
- A request for a unit that does not exist creates it, so a peer that keeps
  changing the unit id grows the store. When the server faces untrusted peers,
  pre-create the units you serve and gate the rest in a `Handler`, or size the
  default unit config and reject unknown units there.
- 0x17 applies the write and reads the registers under one unit lock, exactly as
  the function defines.

### Device identification

`DeviceIdentification` fills the seven identification objects from the strings it
carries; an empty string is left out of the response. A response that does not
fit into `MaxPDU` is split: the server sets `MoreFollows` and `NextObjectID`, and
the client continues with the same read code and that object id. The conformity
level defaults to the read code (0x81 basic, 0x82 regular, 0x83 extended, i.e.
individual access is supported); set `ConformityLevel` to override it.

### Server config

| Field | Meaning | Zero value |
| --- | --- | --- |
| `Network`, `Addr` | transport and address | `NetworkTCP`; address required unless a socket or port is supplied |
| `TLS`, `DTLS` | security config | required for `tls`/`dtls` unless `Listener` is already secure |
| `Listener`, `PacketConn` | already-open socket | nil; the server closes supplied sockets |
| `Serial`, `Port`, `RTU` | serial line and RTU variant | as the client |
| `Handler`, `Store`, `Device` | dispatch stack | no handler; a default store; no device identity |
| `Functions` | vendor function-code registry | built-in codes only |
| `MaxPDU` | inbound PDU limit | `modbus.MaxPDU` |
| `MaxConnections` | concurrent stream connections / datagram peers | unlimited |
| `IdleTimeout` | closes an idle stream connection; forgets an idle datagram peer (not used by the serial transport) | disabled; set it for UDP/DTLS so peers expire |
| `ShutdownTimeout` | bound on `Close` | `DefaultShutdownTimeout` (5s) |
| `Metrics` | per-request hook | none, and it costs nothing |
| `OnError` | transport and handler errors | discard |

## Extension points

Everything a consumer implements or supplies lives in [`hook`](hook/): `Handler` (answer or
take over server requests), `Metrics` and `ServerMetrics` (observability), and `Port` (an
already-open serial line). The root package re-exports every one of them as an alias, so the
two import paths are the same types and existing code needs no change:

```go
var h gmodbus.Handler = hook.HandlerFunc(myHandle) // literally the same type
```

One-closure adapters: `hook.HandlerFunc`, `hook.MetricsFunc`, `hook.ServerMetricsFunc`;
`hook.NoopMetrics` and `hook.NoopServerMetrics` drop every event.

Returning `hook.ErrUnhandled` from a handler asks the server to fall back to the
`MemoryStore`; it is the same sentinel as `gmodbus.ErrUnhandled`, so `errors.Is` matches
either spelling.

To make these switchable at run time — start, stop, inspect — wrap the server in a plugin
registry: `gplugin.NewRegistry(server)` from
[`gplugin`](https://github.com/maxid/gplugin) gives named plugins, a three-state lifecycle,
an observer and per-plugin start/stop; this package only defines what a plugin can plug
into.

## Behaviour

### Transactions and retries

Every MBAP operation takes the next transaction id from a per-client counter
(wraparound tolerated) and rejects a reply whose transaction id or unit id
differs. On the RTU transports there is no transaction id, so a reply must match
the unit id and the function code; the CRC rejects a corrupted frame. All of
those cases are `ErrResponseMismatch`.

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
unusable, the client closes its connection for good: every later call fails fast
with `ErrClosed`. Dial a new client to recover. A datagram transport ignores a
late reply to an earlier exchange and keeps waiting for its own.

### Reconnection

Set `Reconnect` to turn a transport failure or a connection closed by the peer
into an in-place recovery. The client drops the broken connection and dials again
with jittered exponential backoff: `Delay` for the first redial, doubling per
attempt and capped at `MaxDelay`. `MaxAttempts` bounds the dials one operation
may spend. Once a redial succeeds, the in-flight request is retried, so a poller
survives a short outage without seeing an error. The serial port is reopened the
same way.

The whole sequence runs inside the caller's context and holds the single exchange
slot. `Close` interrupts a backoff wait or connect immediately. A permanent
TLS/DTLS configuration failure (`ErrTLSConfig`) ends the sequence at once instead
of spending the budget.

### TLS and DTLS

The client completes the handshake within `DialTimeout`; the server uses the
supplied `*tls.Config` / `*dtls.Config` as given. A certificate that does not
verify, a peer that does not speak the protocol or a security alert wraps
`ErrTLSConfig`, which a reconnect does not retry. A transient handshake failure
(EOF, reset, timeout) is an ordinary transport failure and is retried.

### Errors

| Error | Meaning |
| --- | --- |
| `ErrClosed` | the client has no connection: closed, or out of attempts |
| `ErrTimeout` | every attempt timed out |
| `ErrResponseMismatch` | the reply did not answer the request |
| `ErrTLSConfig` | a permanent TLS/DTLS handshake failure; a retry cannot fix it |
| `ErrUnhandled` | a handler declined a request; fall back to the store |
| `ErrOutOfRange` | a store address or quantity is outside its bank |
| `*gmodbus.Error` | a Modbus exception response; unwraps to `modbus.ErrException` |

Sentinels are wrapped, so test them with `errors.Is`; recover the typed exception
with `errors.As`. Cancelling the caller's context aborts the pending exchange
with `ctx.Err()` and closes the connection.

### Concurrency

A client is safe for concurrent use, but it keeps one request in flight: every
call takes the same slot, and a second caller waits for the current exchange or
for its own context, whichever comes first. A server handles one request at a
time per stream connection; datagram transports handle each packet independently
through gnety sessions.

### Metrics

`Metrics.Request(function, result, duration)` runs once per client attempt, on
the caller's goroutine. `result` is one of `ResultOK`, `ResultException`,
`ResultTimeout`, `ResultTransport` or `ResultMismatch`. A frame the codec rejects
is reported as `ResultTransport` because the connection is dropped.

`ServerMetrics.Request(function, result, duration)` runs once per served request;
`result` is `ServerResultOK`, `ServerResultException` or `ServerResultError`.
Neither hook carries a unit, address or value, by design.

## Examples

Three runnable programs: `server` serves a seeded device, `read-holding` reads a
block of registers over any of the seven transports, and `serial-server`
exercises the RTU paths with no serial device attached.

```sh
go run ./examples/server
go run ./examples/read-holding
go run ./examples/serial-server
```

See [`examples/README.md`](examples/README.md) for what each one answers, the
flags worth copying and the output to expect.

## Interoperability tests

The matrix against implementations that are not this library lives in the
separate [`interop`](interop/README.md) module, so this module's dependency graph
carries no third-party Modbus implementation and no container toolkit. The
default layer is the Go implementation `github.com/simonvetter/modbus` in both
directions; the integration layer is pymodbus (the reference Python stack) in a
container on both sides, plus RTU over a bridged pseudo-terminal pair:

```sh
cd interop && go test ./...                    # another Go implementation, no Docker
cd interop && go test -tags=integration ./...  # pymodbus in a container, and the serial path
```

The interop module replaces this module with the parent directory, so it always
tests the working copy and is not published on its own.

## Scope

The library covers the client and server roles and the seven transports listed
above. Out of scope: RS485 half-duplex echo cancellation, enforcing the RTU 3.5
character silent interval, a serial write timeout (the serial package has no
such setting), function codes 0x07/0x08/0x0B, register maps/device models and a
command line tool.

## Documentation

```sh
go doc -all .
```

## License

MIT. See [LICENSE](LICENSE).
