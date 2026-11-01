[English](CHANGELOG.md) | 简体中文

# 变更日志

> 开发快照不打 tag；以下条目按时间顺序排列。

## [Unreleased]

- 新库：基于 `gnety/codec/modbus` 的 Modbus TCP master。
- `Client` 提供 0x01、0x02、0x03、0x04、0x05、0x06、0x0F、0x10 的类型化操作：`ReadCoils`、`ReadDiscreteInputs`、`ReadHoldingRegisters`、`ReadInputRegisters`、`WriteSingleCoil`、`WriteSingleRegister`、`WriteMultipleCoils`、`WriteMultipleRegisters`。
- 事务处理：自动 transaction id，按 transaction id + unit id 匹配，应答了其它请求时返回 `ErrTransactionMismatch`。
- 每个 client 同时只有一个请求在途：并发调用方在线路上串行，等待当前 exchange 或自己的 context。
- 每次尝试的 `Timeout`，仅对 timeout 与 transport failure 做有界 `Retries`，每次重试新建连接。失败 exchange 关闭连接，后续调用快速返回 `ErrClosed`。
- Modbus exception 响应映射为 `*gmodbus.Error`（unit、function code、exception code），可 unwrap 到 `modbus.ErrException`；exception 从不重试。
- 可选 `Metrics` hook，每次尝试一个事件，低基数 result 标签（`ok`、`exception`、`timeout`、`transport`、`mismatch`），nil-hook 快路径。
- `Poll` runner：校验 `Task` 列表、每任务一个 schedule、错误经 callback 上报、支持取消的 shutdown。
- opt-in `Reconnect` 策略（`ReconnectPolicy`）：transport 失败或 peer close 后丢弃连接，在调用方 context 内按有界抖动指数退避（`Delay`、`MaxDelay`、`MaxAttempts`）重拨并重试 in-flight 请求；nil policy 保持 fail-fast `ErrClosed`；`Close` 中断等待或 dial。
- 内置 dialer 的 `TLS`：handshake 受 `DialTimeout` 约束，失败分类为 `ErrTLSConfig`（永久）或 `ErrTLSHandshake`（瞬时）。
- `Dialer` 支持自定义 transport（TLS、proxy、test double），优先于 `TLS` 并拥有连接。
- 基于 codec 的进程内 fake Modbus TCP server 测试：每个操作的请求解码断言、exception 映射、timeout 重试、重试耗尽、transaction/unit mismatch、关闭 client、并发调用不交错、poller 调度、metrics、reconnect 恢复与放弃、backoff 中 close、有界抖动退避、测试证书 TLS、自定义 dialer。
