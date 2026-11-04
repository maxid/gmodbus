# Roadmap

- [x] Modbus client and server over `gnety/codec/modbus`: TCP, TLS, UDP, DTLS,
      RTU, RTU over TCP and RTU over UDP
- [x] Built-in per-unit register map, `Handler` dispatch and device
      identification
- [x] Function codes 0x01–0x06, 0x0F, 0x10, 0x16, 0x17 and 0x2B/0x0E
- [x] Vendor function-code registry (typed request/response and RTU lengths)
- [x] RTU broadcast, CRC16 and the no-CRC gateway variant
- [x] Transaction matching, bounded retries, exception mapping
- [x] Serialized exchanges for concurrent callers
- [x] Metrics hook with low-cardinality result labels
- [x] Polling runner with per-task schedules
- [x] Injectable transports: `Port` for the serial line, `Listener` and
      `PacketConn` for servers
- [x] Optional automatic recovery for long-lived pollers instead of failing with
      `ErrClosed` after a lost connection
- [x] Per-task scheduling jitter (`Task.Jitter`) to spread a large task list off
      the same instant
- [x] Benchmarks (`BenchmarkReadHolding` over loopback TCP, `BenchmarkRTUFrame`
      for the framing) and an interoperability matrix against reference
      implementations: pymodbus in a container in both directions, and RTU over
      a pseudo-terminal pair
- [ ] An interoperability run against real devices (a PLC or an RTU on a bench);
      the results belong in `docs/PERFORMANCE.md`'s "Real devices" section

Out of scope: RS485 half-duplex echo cancellation, the RTU 3.5-character silent
interval, serial write timeouts, function codes 0x07/0x08/0x0B, device models and
a command line tool.
