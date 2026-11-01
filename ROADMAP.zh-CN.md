[English](ROADMAP.md) | 简体中文

# 路线图

- [x] 基于 `gnety/codec/modbus` 的 Modbus TCP master
- [x] 0x01、0x02、0x03、0x04、0x05、0x06、0x0F、0x10 的类型化读写
- [x] 事务匹配、有界重试、exception 映射
- [x] 并发调用方串行 exchange
- [x] 低基数 result 标签的 metrics hook
- [x] 每任务 schedule 的 polling runner
- [x] 可插拔 dialer 与 connection factory（测试、TLS termination、proxy）
- [x] 长生命周期 poller 的可选自动恢复，而不是断连后直接 `ErrClosed`
- [ ] per-task schedule 抖动，打散大任务列表
- [ ] 与真机的基准和互操作矩阵

明确不在范围：串行 RTU framing、slave/server 角色、register map、设备模型与命令行工具。
