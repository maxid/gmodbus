# Changelog

> Development snapshots are untagged; entries below are chronological.

## [Unreleased]

- **Breaking:** the unified `NewClient(ctx, ClientConfig)` entry point replaces
  `Dial`/`Config`; the client supports TCP, TLS, UDP, DTLS, serial RTU, RTU over
  TCP and RTU over UDP, selected by `Network`. `ErrTransactionMismatch` is now
  `ErrResponseMismatch`, because RTU replies are matched by unit id and function
  code rather than a transaction id.
- New server: `NewServer(ctx, ServerConfig)` serves the same seven transports.
  `Handler` runs first, `MemoryStore` answers the standard function codes per
  unit id, and `DeviceIdentification` answers 0x2B/0x0E. RTU unit 0 is a
  broadcast: writes apply without a reply, reads are refused. A handler panic is
  recovered into a `ServerDeviceFailure` response.
- New `MemoryStore`: independent coils, discrete inputs, holding registers and
  input registers per unit, sized by `UnitConfig`, created lazily, with
  `OnWrite` events outside the lock.
- New function codes: 0x16 Mask Write Register, 0x17 Read/Write Multiple
  Registers and 0x2B/0x0E Read Device Identification, for the client and the
  server. `Client.Execute` sends any registered `modbus.Message`.
- Vendor function codes: `ClientConfig.Functions`/`ServerConfig.Functions`
  decode non-standard codes or replace a standard code's payload, including the
  RTU frame-length hooks needed on stream transports.
- RTU framing with CRC16 (and the `NoCRC` gateway variant) comes from
  `gnety/codec/modbus`; a serial line always verifies the checksum. The built-in
  serial port uses `go.bug.st/serial` and can be replaced with `Port`
  (`net.Conn` satisfies it), so tests run without hardware.
- Transports go through gnety's client/server: in-memory `*tls.Config` and
  `*dtls.Config` are accepted, the client uses `SetDeadline` for a per-attempt
  timeout and cancellation, and streams use `RawHandler` while datagram
  transports use a `DatagramCodec` and sessions.
- `ServerMetrics` with low-cardinality request results, `MaxConnections`,
  `IdleTimeout`, `ShutdownTimeout`, `OnError` and injectable
  `Listener`/`PacketConn`/`Port`.
- Tests cover the seven transports with loopback servers, TLS/DTLS with
  generated certificates, RTU over an in-memory pipe, broadcast, handler
  fallback and panics, unknown function codes, limits and idle recovery, plus
  interoperability with `github.com/simonvetter/modbus` (TCP in both directions,
  and its RTU-over-TCP/RTU-over-UDP/UDP client against this server). The interop
  package is a test-only dependency.

- New library: a Modbus TCP master on `gnety/codec/modbus`.
- `Client` with typed operations for function codes 0x01, 0x02, 0x03, 0x04,
  0x05, 0x06, 0x0F and 0x10: `ReadCoils`, `ReadDiscreteInputs`,
  `ReadHoldingRegisters`, `ReadInputRegisters`, `WriteSingleCoil`,
  `WriteSingleRegister`, `WriteMultipleCoils`, `WriteMultipleRegisters`.
- Transaction handling: automatic transaction ids, matching by transaction id and
  unit id, `ErrTransactionMismatch` on a reply that answers another request.
- One request in flight per client: concurrent callers are serialized on the wire
  and wait for the current exchange or for their own context.
- Per-attempt `Timeout`, bounded `Retries` on timeout and transport failure only,
  and a fresh connection per retry. A failed exchange closes the connection, so a
  later call fails fast with `ErrClosed`.
- Modbus exception responses map to `*gmodbus.Error` (unit, function code,
  exception code) that unwraps to `modbus.ErrException`; exceptions are never
  retried.
- Optional `Metrics` hook, one event per attempt with a low-cardinality result
  label (`ok`, `exception`, `timeout`, `transport`, `mismatch`), and a nil-hook
  fast path.
- `Poll` runner: validated `Task` list, one schedule per task, errors reported
  through a callback, cancellation-aware shutdown.
- Opt-in `Reconnect` policy (`ReconnectPolicy`): after a transport failure or a
  peer close the client drops the connection, redials with bounded, jittered
  exponential backoff (`Delay`, `MaxDelay`, `MaxAttempts`) inside the caller's
  context and retries the in-flight request. A nil policy keeps the fail-fast
  `ErrClosed` behavior; `Close` interrupts a wait or dial.
- `TLS` on the built-in dialer, with the handshake bounded by `DialTimeout` and
  failures classified as `ErrTLSConfig` (permanent) or `ErrTLSHandshake`
  (transient).
- `Dialer` for a custom transport (TLS, proxy, test double); it takes precedence
  over `TLS` and owns the connection.
- Tests against an in-process fake Modbus TCP server built on the codec: every
  operation asserted on the decoded request, exception mapping, retry after a
  timeout, exhausted retries, transaction and unit mismatch, a closed client,
  non-interleaved concurrent callers, poller scheduling, metrics, reconnect
  recovery and give-up, close during backoff, bounded/jittered backoff, TLS with
  a test-generated certificate, and custom dialers.
