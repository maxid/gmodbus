# Changelog

> Development snapshots are untagged; entries below are chronological.

## [Unreleased]

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
- Tests against an in-process fake Modbus TCP server built on the codec: every
  operation asserted on the decoded request, exception mapping, retry after a
  timeout, exhausted retries, transaction and unit mismatch, a closed client,
  non-interleaved concurrent callers, poller scheduling and metrics.
