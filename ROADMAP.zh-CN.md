[English](ROADMAP.md) | 简体中文

# 路线图

- [x] 基于 `gnety/codec/modbus` 的 Modbus 客户端与服务端：TCP、TLS、UDP、DTLS、
      RTU、RTU over TCP、RTU over UDP
- [x] 按 unit 的内置寄存器区、`Handler` 分发与设备标识
- [x] 0x01–0x06、0x0F、0x10、0x16、0x17、0x2B/0x0E
- [x] 厂商功能码注册表（类型化请求/响应与 RTU 帧长）
- [x] RTU 广播、CRC16 与去 CRC 网关变体
- [x] 事务匹配、有界重试、exception 映射
- [x] 并发调用方串行 exchange
- [x] 低基数 result 标签的 metrics hook
- [x] 每任务 schedule 的 polling runner
- [x] 可注入传输：串口 `Port`，服务端 `Listener`/`PacketConn`
- [x] 长生命周期 poller 的可选自动恢复，而不是断连后直接 `ErrClosed`
- [ ] per-task schedule 抖动，打散大任务列表
- [ ] 与真机的基准和互操作矩阵

明确不在范围：RS485 半双工回声消除、RTU 3.5 字符静默间隔、串口写超时、
0x07/0x08/0x0B、寄存器映射/设备模型与命令行工具。
