# Roadmap

- [x] Modbus TCP master over `gnety/codec/modbus`
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

Out of scope, unchanged: serial RTU framing, slave and server roles, register
maps, device models and a command line tool.
