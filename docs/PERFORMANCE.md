English | [简体中文](PERFORMANCE.zh-CN.md)

# Performance baseline

This document records what the committed benchmarks in
[`bench_test.go`](../bench_test.go) measure and what the current tree costs on
one machine. It answers one narrow question — "is this library ever the
bottleneck?" — and deliberately not "how many devices can one process poll?". See
[Limits of these numbers](#limits-of-these-numbers).

Re-measure before drawing conclusions from a different version, machine or Go
toolchain: these are indicative, not a capacity guarantee.

## Environment

| Item | Value |
|---|---|
| Date | 2026-11-04 |
| Go | `go1.27.1` |
| `GOOS` / `GOARCH` | `darwin` / `arm64` |
| CPU | Apple M1 Max, 10 cores (`GOMAXPROCS=10`) |
| Memory | 64 GiB |
| OS | macOS 14.8.9 (Darwin 23J631) |
| Server | in-process, simulated slave (`MemoryStore`), not a PLC |
| Transport | loopback TCP (`127.0.0.1`), one connection, one request in flight |

## Methodology

Two different costs, measured separately:

| Benchmark | What it measures | Topology |
|---|---|---|
| `BenchmarkReadHolding/registers=4` | one `ReadHoldingRegisters` call end to end | client → loopback TCP → in-process server → `MemoryStore` → back |
| `BenchmarkReadHolding/registers=125` | the same call carrying the protocol maximum | as above; the answer is a 255-byte frame |
| `BenchmarkWriteSingleRegister` | one write end to end | as above; one round trip, a smaller PDU |
| `BenchmarkRTUFrame/encode` | PDU encode plus CRC-framed RTU marshal, no socket | none |
| `BenchmarkRTUFrame/decode` | one CRC-framed RTU response decoded into a typed message, no socket | one `bufio.Reader` reused per frame, as a connection does |

- The server is real: `NewServer` binds a loopback socket and answers with the
  built-in register map. Nothing is mocked, and the client's code path is the one
  a deployment uses.
- Connection setup, seeding and one warm-up exchange run before
  `b.ResetTimer()`, so `ns/op` is the steady-state exchange, not a dial.
- `B/op` and `allocs/op` therefore cover **both** sides, because the client and
  its server live in the benchmark process. They are the cost of one exchange,
  not the cost of one library call.
- `BenchmarkRTUFrame` measures the framing arithmetic alone: this is the work the
  library does per frame on a serial line, where the wire is 10 000× slower.

## Results

Median of five runs, `-count=5`, on the machine above.

| Benchmark | `ns/op` | `B/op` | `allocs/op` |
|---|---|---|---|
| `BenchmarkReadHolding/registers=4` | 23 400 | 1 080 | 48 |
| `BenchmarkReadHolding/registers=125` | 25 400 | 2 752 | 169 |
| `BenchmarkWriteSingleRegister` | 22 000 | 992 | 45 |
| `BenchmarkRTUFrame/encode` | 133 | 136 | 6 |
| `BenchmarkRTUFrame/decode` | 268 | 336 | 4 |

Two readings of the same table:

- **A loopback round trip is the floor for a TCP deployment.** ~23 µs is what
  one exchange costs when client and server share the machine and the network is
  the kernel's loopback. A real network adds its own round-trip time on top; the
  library's share of that is the allocation column, not the microsecond column.
- **On a serial line the wire is everything.** At 19200 8N1 one exchange — an
  8-byte request and a 13-byte answer — occupies the line for ~11 ms; at 9600,
  ~22 ms. Encoding that request costs 133 ns and decoding the answer 268 ns, so
  the library spends about 0.004 % of the time the bus does. Optimizing the
  framing further would change nothing a deployment can observe.

## Reproduce

```sh
cd libs/gmodbus
go test -run '^$' -bench 'BenchmarkReadHolding|BenchmarkWriteSingleRegister|BenchmarkRTUFrame' -benchmem -count=5 .
```

A longer serial-line picture needs a real port; `examples/serial-server` runs the
same code over a pseudo-terminal pair when no device is at hand.

## Limits of these numbers

- **The slave is simulated.** `MemoryStore` answers from RAM; a real PLC adds
  device turnaround time, which on a serial bus dominates everything here.
- **One connection, one request in flight.** This library serializes callers on
  one wire by design, so a benchmark with concurrency would measure the queue,
  not the protocol.
- **No TLS or DTLS.** A handshake happens once per connection and a record layer
  adds per-frame work; measure it on the deployment's own certificates if it
  matters.
- **One machine.** The server shares the CPU with the client and with the
  benchmark harness.
- **Allocations are not tuned.** 48 allocations for a four-register read is the
  current cost of one exchange including both sides; a lower number would be a
  welcome pull request, not a promise.

## Real devices

Nothing here was measured against real hardware: that needs someone with a PLC
or an RTU on a bench. If you run one, record it here in this shape and keep the
library version with it:

```
| Date | Device | Transport | Baud | Timeout | Median exchange | Notes |
|------|--------|-----------|------|---------|-----------------|-------|
```

The number worth recording is the **exchange latency** (request sent to answer
decoded), measured the way `BenchmarkReadHolding` does — a warm connection, one
request in flight, the timeout set from the device's own response time. Wire time
from the section above is the floor it cannot beat.
