# Roadmap

- [x] Modbus client and server over `gnety/codec/modbus`: TCP, TLS, UDP, DTLS,
      RTU, RTU over TCP and RTU over UDP
- [x] Built-in per-unit register map, `Handler` dispatch and device
      identification
- [x] Function codes 0x01–0x06, 0x0F, 0x10, 0x16, 0x17 and 0x2B/0x0E
- [x] Vendor function-code registry (typed request/response and RTU lengths)
- [x] RTU broadcast, CRC16 and the no-CRC gateway variant
- [x] Typed reads and writes for function codes 0x01, 0x02, 0x03, 0x04, 0x05,
      0x06, 0x0F, 0x10
- [x] Transaction matching, bounded retries, exception mapping
- [x] Serialized exchanges for concurrent callers
- [x] Metrics hook with low-cardinality result labels
- [x] Polling runner with per-task schedules
- [x] Pluggable dialer and connection factory (tests, TLS termination, proxies)
- [x] Optional automatic recovery for long-lived pollers instead of failing with
      `ErrClosed` after a lost connection
- [ ] Per-task scheduling jitter to spread a large task list
- [ ] Benchmarks and an interoperability matrix against real devices

Out of scope: RS485 half-duplex echo cancellation, the RTU 3.5-character silent
interval, serial write timeouts, function codes 0x07/0x08/0x0B, device models and
a command line tool.
