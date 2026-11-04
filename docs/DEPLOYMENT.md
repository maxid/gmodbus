English | [简体中文](DEPLOYMENT.zh-CN.md)

# Deployment: what to set, and what breaks first

> Applies to the current tree (2026-11-04). Numbers are the library's defaults,
> not a recommendation: a field bus is slower and noisier than a loopback. See
> [ARCHITECTURE.md](ARCHITECTURE.md) for where each piece lives.

This library is deployed in one of two roles: a **master** that polls devices
(a gateway, a collector, a simulator's peer) or a **slave** that answers them
(an edge device, a test double, a protocol bridge). They share the transport
table and the error values, and almost nothing else.

## The serial line

| Parameter | Zero value | What to set it to |
|---|---|---|
| `Serial.Baud` | 19200 | What the device's manual says: 9600 and 19200 are the common cases. Every node on one bus must agree. |
| `Serial.DataBits` | 8 | 8 unless the device is older than RTU itself. |
| `Serial.Parity` | none | **Most devices use 8E1, not 8N1.** An RTU device whose parity differs answers with garbage, which shows up as a CRC error, not as a timeout. |
| `Serial.StopBits` | 1 | 1 for 8E1; the specification mandates 2 on a no-parity line, and many devices keep 1 anyway. |

The zero value is 19200 8N1 on purpose: it is the most forgiving starting point,
not the specification's default. Copy the device's own sheet.

Two timing facts decide `Timeout`:

- One character is `1 + data bits + parity + stop bits` bits long: 10 bits for
  8N1, 11 for 8E1 and 8N2. At 9600 baud that is 1.04 ms per character; at 19200,
  0.52 ms.
- A frame is `1 + len(PDU) + 2` bytes. A four-register read is 8 bytes out
  (8.3 ms at 9600) and 13 bytes back (13.5 ms at 9600). The largest read the
  protocol allows, 125 registers, comes back in a 255-byte frame: ~133 ms at
  19200.

So a `Timeout` of 300–500 ms covers a short exchange on a quiet bus; use 1–2 s on
a 9600 bus with long frames or a device known to think slowly. Anything below
~50 ms is measuring the device's turnaround, not the line.

Not modeled here, and therefore the hardware's job: RS485 half-duplex echo
cancellation, the 3.5-character silent interval between frames, and a serial
write timeout. Use a converter with automatic direction control (DE/RE) and
accept that a write blocks until the driver returns.

## Timeouts, retries and the polling budget

| Field | Default | Meaning |
|---|---|---|
| `ClientConfig.DialTimeout` | 5s | one connect plus the TLS/DTLS handshake |
| `ClientConfig.Timeout` | 5s | one attempt: the write plus the response read |
| `ClientConfig.Retries` | 0 | extra attempts after a **timeout or transport failure** |
| `ReconnectPolicy.Delay` / `MaxDelay` / `MaxAttempts` | 100ms / 5s / 5 | bounded, jittered exponential backoff for a long-lived client |

An attempt's worst case is `Timeout`; a call's worst case is
`(Retries+1) × (Timeout + DialTimeout)`. A polling task's `Every` must be larger
than that, or the task spends its life in the retry path:

```go
cli, _ := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
    Network: gmodbus.NetworkRTU,
    Addr:    "/dev/ttyUSB0",
    Serial:  &gmodbus.SerialConfig{Baud: 9600, Parity: gmodbus.EvenParity},
    Timeout: 500 * time.Millisecond,
    Retries: 2,
    Reconnect: &gmodbus.ReconnectPolicy{}, // a poller must survive a lost link
})

err := cli.Poll(ctx, []gmodbus.Task{{
    Name:   "tank-level",
    Every:  5 * time.Second,                  // > 3 × (500ms + dial)
    Jitter: 500 * time.Millisecond,           // spread a long task list
    Do:     func(ctx context.Context) error { _, err := cli.ReadHoldingRegisters(ctx, 3, 0, 4); return err },
}}, func(task gmodbus.Task, err error) { log.Printf("poll %s: %v", task.Name, err) })
```

Two rules that matter in the field:

- **`Retries` does not cover exceptions.** A device answering `IllegalDataAddress`
  is telling you the request is wrong; retrying it five times only adds traffic.
- **A long task list needs `Task.Jitter`.** Without it every task reads at t=0
  and again on every common multiple of their periods, and the queue on the one
  exchange slot — not the devices — decides who waits. A value around one poll
  period spreads them evenly; the period itself stays exact.
- **Do not stack `Retries` and a `ReconnectPolicy`.** With a policy, a transport
  failure spends the policy's budget; `Retries` then only governs timeouts. Pick
  one recovery story per deployment.

## Gateways

A gateway that speaks RTU on one side and Modbus TCP on the other is the most
common deployment, and the transport choice is the whole configuration:

| Gateway behaviour | Client configuration |
|---|---|
| Forwards RTU frames unchanged over TCP | `NetworkRTUOverTCP`, `RTU` zero value (CRC kept) |
| Strips the CRC before wrapping frames | `NetworkRTUOverTCP`, `RTU: modbus.RTUOptions{NoCRC: true}` |
| One frame per UDP datagram | `NetworkRTUOverUDP`, discourages retries on a lossy path |
| Translates RTU to MBAP (unit ids mapped) | `NetworkTCP`, and the gateway owns the unit mapping |

Unit addressing stays the integrator's business: the library takes a `uint8`, the
serial specification reserves 1–247 for slaves, 0 for broadcast and 248–255 for
later use. Two devices with the same unit id on one bus answer each other's
requests — the failure mode is `ErrResponseMismatch` or a CRC error, never a
timeout, which is why it is worth checking the bus map before the code.

Broadcast (unit 0, RTU only) is a write that is applied and never answered. Use
it for a fan-out setpoint; never for a read, and never when the result matters.

## The server side

```go
srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
    Addr:            ":502",
    Store:           store,
    Device:          &gmodbus.DeviceIdentification{VendorName: "ACME", ProductCode: "EDGE-1"},
    MaxConnections:  64,
    IdleTimeout:     2 * time.Minute,
    ShutdownTimeout: 5 * time.Second,
    OnError:         func(err error) { log.Printf("server: %v", err) },
})
```

- Port 502 is privileged on Unix; a container or a systemd unit with a capability
  is simpler than running the process as root.
- `IdleTimeout` closes a stream connection that sends nothing; on a polled bus a
  client that connects once and holds the connection for hours is normal, so keep
  it well above the poll interval or leave it at zero.
- `MaxConnections` bounds concurrent stream connections and datagram peers; a
  Modbus master opens one connection per polling process, so the number is small.
- Every request is stateless, so a TCP load balancer may distribute connections
  freely; there is no session to pin.
- A clean client disconnect is not reported through `OnError`; a malformed frame
  is. That split is what keeps the hook useful.

## Security

Modbus itself has no authentication and no encryption. On the wire, anyone who
can reach port 502 can write coils:

- `NetworkTLS` for Modbus TCP over TLS: present a certificate to the client and
  verify the server's with `ClientConfig.TLS`.
- `NetworkDTLS` for datagram deployments; `ClientConfig.DTLS` is required, and a
  bad certificate is classified as `ErrTLSConfig` (permanent) rather than burned
  through the retry budget.
- Serial links are physically trusted; an RS485 segment that leaves the cabinet
  is not. Bridge it through a gateway you control rather than exposing the bus.
- Never expose 502 to an untrusted network, TLS or not: a VPN or an allowlist is
  the boundary, not the protocol.

## Failure modes

What you see, what it means, what to change:

| Symptom / error | Cause | First move |
|---|---|---|
| `ErrTimeout` after N attempts | no complete response within `Timeout`, every time | raise `Timeout`, confirm baud/parity/stop bits with a serial sniffer, confirm the unit id and that the device is powered |
| wrapped transport error, then `ErrClosed` | the link broke and the retry budget was spent | a `ReconnectPolicy` for long-lived clients; check cabling and the converter's power |
| `ErrResponseMismatch` | a reply for another transaction, unit or function | two devices with the same unit id; a stale reply left by a datagram timeout; a peer that is not Modbus |
| `*gmodbus.Error` code `0x01` | the device does not implement that function code | check the device's function-code list |
| `*gmodbus.Error` code `0x02` | the address or range is not implemented | check the register map and the device's addressing base (0 vs 1) |
| `*gmodbus.Error` code `0x03` | quantity or value outside the device's limits | respect the per-function maximums (125 registers or 2000 bits for a read, 123 registers or 1968 bits for a write) |
| `*gmodbus.Error` code `0x04` | the device failed while processing | device-side; log the unit and address, retry later |
| `*gmodbus.Error` code `0x05` / `0x06` | acknowledge / busy | retry at the application level after a delay; the library never retries an exception |
| `*gmodbus.Error` code `0x0A` / `0x0B` | the gateway could not reach the target | gateway routing and unit mapping |
| `wire: malformed input: ... CRC` | framing or checksum mismatch | parity and stop bits, RS485 termination and bias, a `NoCRC` setting that does not match the gateway |
| serial open failure | wrong device path, permissions, or a driver that is already open | `/dev/serial/by-id/...` on Linux, `dialout` group membership, one process per port |
| `ErrTLSConfig` | certificate verification failed | CA bundle, host name, clock |
| `ErrTLSHandshake` in `Metrics` as `transport` | the peer spoke TLS and fell over | server certificate chain and the middlebox in between |
| `ErrOutOfRange` from `MemoryStore` | address or quantity outside the configured bank | size `UnitConfig`, or map the request in a `Handler` |

## Observability

- `ClientConfig.Metrics` reports one event per attempt with a low-cardinality
  result (`ok`, `exception`, `timeout`, `transport`, `mismatch`). Alert on
  `timeout` and `transport` separately: a timeout is a device or line problem, a
  transport failure is the connection.
- `ServerConfig.Metrics` reports `ok`, `exception`, `error`. A rising `exception`
  rate is a configuration change on the client side, not a server fault.
- `ServerConfig.OnError` carries transport errors and handler failures; a
  handler panic is reported there and answered with `ServerDeviceFailure`.
- Neither hook carries a unit id, an address or a value — by design. If a
  deployment needs per-device metrics, wrap the operations, not the hook.

## Deploy checklist

1. `Timeout` and `Every` chosen from the line math above, `Every` larger than the
   worst-case call, and a `Jitter` on every task when the list is long.
2. One recovery story: `Retries` for a short-lived process, a `ReconnectPolicy`
   for a poller.
3. Serial parameters copied from the device sheet, not from this document.
4. A health check that reads one known register and distinguishes a timeout from
   an exception.
5. `OnError` wired on the server side and `Metrics` on the client side.
6. Shutdown path: cancel the context, `Client.Close` / `Server.Close`, and a
   `ShutdownTimeout` that fits the supervisor's grace period.

Versioning and the upgrade rules are in [VERSIONING.md](../VERSIONING.md); every
breaking change is marked **Breaking** in the [CHANGELOG](../CHANGELOG.md).
