# Changelog

> Development snapshots are untagged; entries below are chronological.

## [Unreleased]

### Changed

- **The extension contracts moved to `gmodbus/hook`.** `Handler`, `HandlerFunc`, `Metrics`,
  `ServerMetrics`, `Port`, the `Result*`/`ServerResult*` labels and the `ErrUnhandled`
  sentinel are defined there and re-exported by the root package as aliases, so every
  existing import, name and `errors.Is` check keeps working. The contracts now form one
  reviewable directory, and `gmodbus/hook` imports nothing from the root package.

### Added

- `hook.MetricsFunc` and `hook.ServerMetricsFunc`: a one-closure hook needs no named type.

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
  tests live in a separate `interop` module, so the library's module graph
  carries no third-party Modbus implementation.
- Fixes after review: a split device-identification response continues from the
  requested object instead of restarting at the first one, and the conformity
  level follows the read code (overridable with
  `DeviceIdentification.ConformityLevel`); a malformed RTU broadcast frame is no
  longer answered; the handler context is cancelled on `Close` and a handler
  panic is counted as `ServerResultError`; the serial loop backs off after a read
  failure and `Close` is bounded by `ShutdownTimeout`; broadcast is limited to
  the state-changing write codes (0x17 is refused); `ErrServerClosed`, which was
  never returned, is removed.

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
- The interoperability matrix gained its integration layer, behind
  `-tags=integration` in the `interop` module: pymodbus, the reference Python
  stack, as a slave in a container for our client and as a client against our
  server, plus RTU over a bridged pseudo-terminal pair (two pty pairs, no
  hardware). The image is built from `interop/docker/pymodbus` on first use and
  every case skips when no Docker daemon answers; the existing in-process matrix
  against `github.com/simonvetter/modbus` still runs in the default suite.
- Fuzz targets for the two parsers a peer controls: `FuzzDecodeADU` feeds
  arbitrary bytes to the client's stream decoder in both framings (MBAP and
  CRC-framed RTU) and pins "no panic, a rejected frame yields no message, an
  accepted one re-encodes to the exact bytes it came from, and a one-register
  read never decodes more than one register"; `FuzzDecodePDU` dispatches
  arbitrary PDUs through the server and pins that every answer carries the
  request's function code or its exception form and can be encoded. CI runs each
  for ten seconds.
- CI gained the `integration` job (the reference implementations and the serial
  link) next to the existing `race` and `fuzz` ones, so the four gates of the
  research baseline are all run by the workflow.
- `docs/` exists: `ARCHITECTURE.md` (package map, the three transport families,
  the client and server state machines, the wire boundary with gnety, and where
  a new function code belongs) and `DEPLOYMENT.md` (serial parameters, the
  timeout and retry budget, gateway configurations, failure modes and a deploy
  checklist), both with a Chinese edition. The main README now links the
  documentation instead of only pointing at `go doc`.
- Examples: `examples/serial-server` runs an RTU slave and an RTU master against
  each other over loopback TCP or a socat pseudo-terminal pair, which is how the
  RTU paths are exercised with no serial device attached. `read-holding` and
  `server` now share one convention — `-h` with copyable commands, `[n/N]`
  steps, `[done]`, a `[stop]` line on Ctrl-C and exit code 0 — and
  `examples/README.md` (with a Chinese edition) says what each one answers.

### Fixed

- A request whose function code already carries the exception bit (0x83) is
  answered with a legal IllegalFunction exception instead of an unencodable one
  that dropped the connection: the base code is what an exception frame stores,
  and the encoder sets the bit.
- A serial line opened with `SerialConfig.StopBits` at `OneStopBit` (the default)
  asked the driver for **two** stop bits, and `TwoStopBits` was rejected as
  invalid. The public type counts the bits while `go.bug.st/serial` enumerates
  them, so the value has to be mapped, not cast.
- A client that closes its connection no longer reaches `OnError`: an end at a
  frame boundary (`io.EOF`) or in the middle of a frame (`wire.ErrIncomplete`)
  is a disconnect, not a server fault, and half the log lines of a busy server
  used to be those. A frame that arrives complete and malformed is still
  reported.
