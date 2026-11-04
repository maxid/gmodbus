[English](CHANGELOG.md) | 简体中文

# 变更日志

> 开发快照不打 tag；以下条目按时间顺序排列。

## [Unreleased]

### 变更

- **扩展契约移到 `gmodbus/hook`。** `Handler`、`HandlerFunc`、`Metrics`、`ServerMetrics`、
  `Port`、`Result*`/`ServerResult*` 标签与 `ErrUnhandled` sentinel 定义在该包，根包以类型别名
  重新导出，因此已有的 import、名称与 `errors.Is` 判断全部照旧。契约从两个实现文件的一节变成
  单独可评审的目录，且 `gmodbus/hook` 不 import 根包。

### 新增

- `hook.MetricsFunc` 与 `hook.ServerMetricsFunc`：一个闭包即可当钩子，不必起名字类型。

- `Task.Jitter`：被轮询的任务可以按该抖动内的随机偏移延后第一次运行，而不是立即开始——这是大任务列表不再在同一瞬间全发、挤在客户端单一交换槽上的办法。只抖动起点，周期仍精确等于 `Every`；零值保持立即开始；抖动大于间隔会被拒绝。

- **破坏性变更：**统一入口 `NewClient(ctx, ClientConfig)` 取代 `Dial`/`Config`；客户端按 `Network` 支持 TCP、TLS、UDP、DTLS、串口 RTU、RTU over TCP 与 RTU over UDP。`ErrTransactionMismatch` 改为 `ErrResponseMismatch`，因为 RTU 应答按 unit id 与功能码匹配，没有 transaction id。
- 新增服务端：`NewServer(ctx, ServerConfig)` 提供同样七种传输。`Handler` 优先，`MemoryStore` 按 unit 应答标准功能码，`DeviceIdentification` 应答 0x2B/0x0E。RTU unit 0 是广播：写生效不回包，读被拒绝；Handler panic 恢复为 `ServerDeviceFailure`。
- 新增 `MemoryStore`：每个 unit 独立的线圈、离散输入、保持寄存器、输入寄存器，按 `UnitConfig` 定容、懒创建，`OnWrite` 在锁外回调。
- 新增功能码：0x16 掩码写、0x17 读写多寄存器、0x2B/0x0E 读设备标识，客户端与服务端都覆盖；`Client.Execute` 可发送任意已注册的 `modbus.Message`。
- 厂商功能码：`ClientConfig.Functions`/`ServerConfig.Functions` 解码非标功能码或替换标准码载荷，含流式传输所需的 RTU 帧长回调。
- RTU 分帧与 CRC16（及去 CRC 的 `NoCRC` 网关变体）来自 `gnety/codec/modbus`；串口始终校验 CRC。内置串口用 `go.bug.st/serial`，可用 `Port` 替换（`net.Conn` 满足），测试不需要硬件。
- 传输走 gnety 的 client/server：接受内存态 `*tls.Config`/`*dtls.Config`，客户端用 `SetDeadline` 实现单次尝试超时与取消；流传输用 `RawHandler`，数据报用 `DatagramCodec` 与 session。
- `ServerMetrics` 低基数请求结果，另有 `MaxConnections`、`IdleTimeout`、`ShutdownTimeout`、`OnError` 与可注入的 `Listener`/`PacketConn`/`Port`。
- 测试覆盖七种传输回环、TLS/DTLS 自签证书、`net.Pipe` 上的 RTU、广播、Handler 回落与 panic、未知功能码、限额与空闲恢复，以及与 `github.com/simonvetter/modbus` 的互操作（TCP 双向，以及对方 RTU-over-TCP/RTU-over-UDP/UDP client 对本站 server）。互操作测试放在独立的 `interop` 模块，本库的依赖图不携带第三方 Modbus 实现。
- 评审修复：设备标识分片后按请求的对象续读而不是从头重来，符合级别按 read code 推导（`DeviceIdentification.ConformityLevel` 可覆盖）；RTU 广播的坏帧不再回包；Handler context 在 `Close` 时取消，Handler panic 记为 `ServerResultError`；串口读失败后加入退避，`Close` 受 `ShutdownTimeout` 约束；广播只允许会改变状态的写功能码（0x17 被拒绝）；删除从未使用的 `ErrServerClosed`。

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
- 互操作矩阵补上集成层（`interop` 模块的 `-tags=integration`）：pymodbus（Python 参考实现）在容器里当从站给我们的客户端用、也当客户端打我们的服务端；另有对接伪终端对的 RTU 串口路径（两对 pty，不需要硬件）。镜像首次使用时从 `interop/docker/pymodbus` 构建，Docker 不可用时逐例 skip；原有对 `github.com/simonvetter/modbus` 的进程内矩阵仍在默认套件里。
- 两个解析入口的 fuzz：`FuzzDecodeADU` 把任意字节喂给客户端流式解码器（MBAP 与 CRC 帧 RTU 两种分帧），钉住“不 panic、被拒的帧不产出消息、被接受的帧重编码后与来源字节一致、读 1 个寄存器的请求解不出多于 1 个寄存器”；`FuzzDecodePDU` 把任意 PDU 送进服务端分派，钉住“应答必然带请求的功能码或其异常形式、且能编码”。CI 各跑 10 秒。
- CI 在 `race`、`fuzz` 之外再加 `integration` 作业（参考实现与串口链路），研发基线的四道闸全部由工作流跑起来。
- 基准覆盖部署在意的两种花费：`BenchmarkReadHolding`（4 与 125 寄存器）与 `BenchmarkWriteSingleRegister` 走真实回环 TCP、服务端同进程；`BenchmarkRTUFrame`（编码/解码）只测分帧。`docs/PERFORMANCE.md` 记录一台机器上的数字、它们意味着什么与不意味着什么，以及真实设备结果的模板——硬件数据本轮没有。
- 新增 `docs/`：`ARCHITECTURE`（包地图、三类传输、客户端与服务端状态机、与 gnety 的线格式边界、新功能码该落在哪）与 `DEPLOYMENT`（串口参数、超时与重试预算、网关配置、失败模式与部署清单），均含中文版。主 README 改为链接文档，而不只是 `go doc`。
- 示例：`examples/serial-server` 让一个 RTU 从站与一个 RTU 主站互相对打（回环 TCP，或 socat 伪终端对），这是没有串口设备时验证 RTU 路径的做法。`read-holding` 与 `server` 统一了约定——`-h` 给出可复制命令、`[n/N]` 步骤、`[done]`、Ctrl-C 打 `[stop]` 且退出码 0——并新增 `examples/README.md`（含中文版）说明每个示例回答什么问题。

### 修复

- 功能码已带异常位的请求（0x83）改用合法的 IllegalFunction 应答，不再构造出无法编码的异常帧把连接丢掉：异常帧里存的是基础功能码，发送时才置异常位。
- `SerialConfig.StopBits` 取 `OneStopBit`（默认值）时实际向驱动要了 **两个** 停止位，而 `TwoStopBits` 被判为非法：公开类型数的是位数，`go.bug.st/serial` 用的是枚举值，必须映射不能直接转换。
- 客户端正常断开不再进 `OnError`：帧边界上的 `io.EOF` 与帧中间的 `wire.ErrIncomplete` 是断开而不是服务端故障（此前繁忙服务端的日志里一半是它们）；完整收到但格式错的帧仍然上报。
