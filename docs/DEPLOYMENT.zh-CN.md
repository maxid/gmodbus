[English](DEPLOYMENT.md) | 简体中文

# 部署：要调什么，先坏什么

> 适用于当前代码树（2026-11-04）。文中的数字是库的默认值，不是推荐值：现场总线比回环慢，也
> 更吵。每块代码住在哪里见 [ARCHITECTURE.zh-CN.md](ARCHITECTURE.zh-CN.md)。

这个库按两种角色部署：**主站**（轮询设备：网关、采集器、模拟器的对端）或**从站**（应答它们：
边缘设备、测试替身、协议桥）。两者共享传输表和错误值，其余几乎不共享。

## 串口线路

| 参数 | 零值 | 该设成什么 |
|---|---|---|
| `Serial.Baud` | 19200 | 设备手册写的值：9600 与 19200 最常见。同一条总线上的所有节点必须一致。 |
| `Serial.DataBits` | 8 | 8，除非设备比 RTU 还老。 |
| `Serial.Parity` | 无 | **多数设备是 8E1，不是 8N1。** 校验位对不上的 RTU 设备回的是乱码，表现为 CRC 错误而不是超时。 |
| `Serial.StopBits` | 1 | 8E1 就是 1；规范要求无校验的线路上用 2，而很多设备仍然用 1。 |

零值是 19200 8N1 是刻意选的：它是最宽容的起点，不是规范的默认。以设备手册为准。

`Timeout` 由两个时间事实决定：

- 一个字符是 `1 + 数据位 + 校验位 + 停止位` 位：8N1 是 10 位，8E1 与 8N2 是 11 位。9600 下
  每字符 1.04 ms，19200 下 0.52 ms。
- 一帧是 `1 + len(PDU) + 2` 字节。读 4 个寄存器：出去 8 字节（9600 下 8.3 ms），回来 13
  字节（9600 下 13.5 ms）。协议允许的最大读——125 个寄存器——回来的帧是 255 字节，19200 下
  约 133 ms。

所以 `Timeout` 取 300–500 ms 足以覆盖安静总线上的短交换；9600 的长帧或反应慢的设备用 1–2 s。
低于 50 ms 的取值测的是设备的转向时间，不是线路。

这里不建模、因此归硬件负责的三件事：RS485 半双工回显抵消、帧间 3.5 字符静默间隔、串口写超时。
用带自动收发方向控制（DE/RE）的转换器，并接受“写在驱动返回前一直阻塞”。

## 超时、重试与轮询预算

| 字段 | 默认 | 含义 |
|---|---|---|
| `ClientConfig.DialTimeout` | 5s | 一次连接加 TLS/DTLS 握手 |
| `ClientConfig.Timeout` | 5s | 一次尝试：写加读应答 |
| `ClientConfig.Retries` | 0 | **超时或传输失败**之后的额外尝试次数 |
| `ReconnectPolicy.Delay` / `MaxDelay` / `MaxAttempts` | 100ms / 5s / 5 | 长期运行客户端的有界抖动指数退避 |

一次尝试的最坏情况是 `Timeout`；一次调用的最坏情况是
`(Retries+1) × (Timeout + DialTimeout)`。轮询任务的 `Every` 必须比它大，否则任务一辈子都待在
重试路径里：

```go
cli, _ := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
    Network: gmodbus.NetworkRTU,
    Addr:    "/dev/ttyUSB0",
    Serial:  &gmodbus.SerialConfig{Baud: 9600, Parity: gmodbus.EvenParity},
    Timeout: 500 * time.Millisecond,
    Retries: 2,
    Reconnect: &gmodbus.ReconnectPolicy{}, // 轮询方必须能扛住掉线
})

err := cli.Poll(ctx, []gmodbus.Task{{
    Name:  "tank-level",
    Every: 5 * time.Second, // > 3 × (500ms + dial)
    Do:    func(ctx context.Context) error { _, err := cli.ReadHoldingRegisters(ctx, 3, 0, 4); return err },
}}, func(task gmodbus.Task, err error) { log.Printf("poll %s: %v", task.Name, err) })
```

现场有两条规则必须记住：

- **`Retries` 不覆盖异常应答。** 设备回 `IllegalDataAddress` 是在说请求本身错了，重试五次只会
  多产生流量。
- **别把 `Retries` 与 `ReconnectPolicy` 叠着用。** 有策略时，传输失败花的是策略的预算，
  `Retries` 只再管超时。每次部署只选一种恢复故事。

## 网关

一侧讲 RTU、另一侧讲 Modbus TCP 的网关是最常见的部署，传输选择就是全部配置：

| 网关行为 | 客户端配置 |
|---|---|
| 原样转发 TCP 上的 RTU 帧 | `NetworkRTUOverTCP`，`RTU` 零值（保留 CRC） |
| 包帧之前把 CRC 去掉 | `NetworkRTUOverTCP`，`RTU: modbus.RTUOptions{NoCRC: true}` |
| 每个 UDP 数据报一帧 | `NetworkRTUOverUDP`，在有丢包的路上不鼓励重试 |
| RTU 翻成 MBAP（映射 unit） | `NetworkTCP`，unit 映射由网关负责 |

unit 编址仍归集成方：库收 `uint8`，串口规范把 1–247 留给从站、0 给广播、248–255 留作后用。
同一条总线上两台同 unit 的设备会互相应答对方的请求——症状是 `ErrResponseMismatch` 或 CRC
错误，绝不会有超时，所以写代码之前先核对总线地址表。

广播（unit 0，仅 RTU）是“写了不回包”。用于一次下发设定值；不要用于读，也不要在意结果时用。

## 服务端

```go
srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
    Addr:            ":502",
    Store:           store,
    Device:          &gmodbus.DeviceIdentification{VendorName: "ACME", ProductCode: "EDGE-1"},
    MaxConnections:  64,
    IdleTimeout:     2 * time.Minute,
    ShutdownTimeout: 5 * time.Second,
    OnError:         func(err error) { log.Printf("server: %v", err) },
})
```

- 502 端口在 Unix 上是特权端口；用容器或给 systemd 单元加 capability，比用 root 跑进程简单。
- `IdleTimeout` 关闭不发请求的流连接；在被轮询的总线上，客户端连一次挂几小时是常态，所以要么
  把它设得远大于轮询周期，要么留 0。
- `MaxConnections` 约束并发流连接与数据报 peer；一个 Modbus 主站每个轮询进程开一条连接，这个
  数通常很小。
- 每个请求都是无状态的，因此 TCP 负载均衡可以随意分发连接；没有需要粘住的会话。
- 客户端正常断开不进 `OnError`，坏帧才进。这个区分正是该钩子有用的原因。

## 安全

Modbus 本身没有认证，也没有加密。线路上只要能到 502 端口的人就能写线圈：

- `NetworkTLS`：Modbus TCP over TLS；客户端用 `ClientConfig.TLS` 校验服务端证书。
- `NetworkDTLS`：数据报部署；`ClientConfig.DTLS` 必填，证书错误归类为 `ErrTLSConfig`（永久），
  不会去烧重试预算。
- 串口线路按“物理可信”对待；离开机柜的 RS485 段不是。用你控制的网关桥接，而不是把总线暴露。
- 无论如何不要把 502 暴露到不可信网络：边界是 VPN 或白名单，不是协议本身。

## 失败模式

看到什么、意味着什么、先改什么：

| 症状 / 错误 | 原因 | 首先做什么 |
|---|---|---|
| 若干次尝试后的 `ErrTimeout` | 每次都等不到完整应答 | 调大 `Timeout`；用串口嗅探器核对波特率/校验位/停止位；确认 unit 号与设备供电 |
| 被包装的传输错误，然后 `ErrClosed` | 链路断了且重试预算用尽 | 长期运行的客户端加 `ReconnectPolicy`；检查线缆与转换器供电 |
| `ErrResponseMismatch` | 应答属于另一个事务、unit 或功能码 | 同 unit 的两台设备；数据报超时后残留的旧应答；对端根本不是 Modbus |
| `*gmodbus.Error` 码 `0x01` | 设备不支持该功能码 | 查设备的功能码清单 |
| `*gmodbus.Error` 码 `0x02` | 地址或范围未实现 | 查寄存器表与设备的编址基准（0 还是 1） |
| `*gmodbus.Error` 码 `0x03` | 数量或值超出设备限制 | 遵守每功能码上限（读 125 寄存器或 2000 位，写 123 寄存器或 1968 位） |
| `*gmodbus.Error` 码 `0x04` | 设备处理时故障 | 设备侧；记下 unit 与地址，稍后重试 |
| `*gmodbus.Error` 码 `0x05` / `0x06` | 确认 / 忙 | 应用层延迟后重试；库从不重试异常 |
| `*gmodbus.Error` 码 `0x0A` / `0x0B` | 网关到不了目标 | 网关路由与 unit 映射 |
| `wire: malformed input: ... CRC` | 分帧或校验不匹配 | 校验位与停止位、RS485 终端与偏置、`NoCRC` 是否与网关一致 |
| 串口打开失败 | 设备路径错、权限不足、或驱动已被占用 | Linux 上用 `/dev/serial/by-id/...`，加入 `dialout` 组，一个端口只给一个进程 |
| `ErrTLSConfig` | 证书校验失败 | CA 包、主机名、时钟 |
| `Metrics` 里记为 `transport` 的 `ErrTLSHandshake` | 对端说了 TLS 又崩了 | 服务端证书链与中间设备 |
| `MemoryStore` 的 `ErrOutOfRange` | 地址或数量超出配置的区 | 调整 `UnitConfig`，或在 `Handler` 里映射请求 |

## 可观测性

- `ClientConfig.Metrics` 每次尝试一个事件，低基数结果（`ok`、`exception`、`timeout`、
  `transport`、`mismatch`）。`timeout` 与 `transport` 要分开告警：超时是设备或线路问题，
  传输失败是连接问题。
- `ServerConfig.Metrics` 报 `ok`、`exception`、`error`。`exception` 升高通常是客户端侧的配置
  变更，不是服务端故障。
- `ServerConfig.OnError` 承载传输错误与 handler 失败；handler panic 也从这里上报，并回
  `ServerDeviceFailure`。
- 两个钩子都不带 unit、地址或值——这是设计。要按设备做指标就包住操作，而不是改钩子。

## 部署清单

1. `Timeout` 与 `Every` 按上面的线路算术定，`Every` 大于最坏情况的一次调用。
2. 只选一种恢复故事：短生命周期进程用 `Retries`，轮询方用 `ReconnectPolicy`。
3. 串口参数抄设备手册，不抄本文。
4. 健康检查读一个已知寄存器，并区分超时与异常。
5. 服务端接上 `OnError`，客户端接上 `Metrics`。
6. 关停路径：取消 context、`Client.Close` / `Server.Close`，`ShutdownTimeout` 与监管进程的
   宽限期匹配。

版本与升级规则见 [VERSIONING.zh-CN.md](../VERSIONING.zh-CN.md)；每个破坏性变更都在
[CHANGELOG.zh-CN.md](../CHANGELOG.zh-CN.md) 里标了**破坏性变更**。
