[English](README.md) | 简体中文

# gmodbus

Go 的 **Modbus 客户端与服务端**，线格式来自 [gnety](https://github.com/maxid/gnety)
的类型化 Modbus codec（`codec/modbus`）。

> 状态：**开发快照（未打标签）** — `0.0.0-dev`。线格式在 codec；本库负责连接、事务、
> 重试、异常码映射、轮询调度与服务端分发。

## 特性

- **七种传输**：TCP、TLS over TCP、UDP、DTLS、串口 RTU、RTU over TCP、RTU over UDP，
  客户端与服务端都覆盖。
- **类型化客户端**：功能码 0x01/0x02/0x03/0x04/0x05/0x06/0x0F/0x10，以及 0x16、
  0x17、0x2B/0x0E。
- **自带寄存器区的服务端**：线圈、离散输入、保持寄存器、输入寄存器按 unit 独立保存；
  `Handler` 可接管任意请求。
- **厂商功能码**：类型化注册表可解码非标功能码，也可替换标准功能码的载荷，无需改库。
- 自动事务号与严格匹配；不回答本请求的响应会被拒绝。
- `Timeout` 约束单次尝试，`Retries` 约束超时/传输失败后的重试；Modbus 异常响应是
  “已送达的答案”，映射成 `*gmodbus.Error`，永不重试。
- 可选自动重连：有界、带抖动的指数退避。
- RTU 默认 CRC16；RTU over TCP/UDP 可对去 CRC 的网关关闭校验，串口始终保留 CRC。
- RTU 广播（unit 0）：写生效且不回包；读被拒绝。
- 低基数客户端/服务端指标钩子，nil 时零成本。
- 独立调度的轮询运行器。

## 安装

```sh
go get github.com/maxid/gmodbus@<commit>
```

开发期不打 tag，使用 commit 伪版本。

## 快速开始

```go
cli, err := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
    Addr:    "10.0.0.5:502", // Network 零值即 NetworkTCP
    Timeout: time.Second,
    Retries: 2,
})
if err != nil {
    return err
}
defer cli.Close()

registers, err := cli.ReadHoldingRegisters(ctx, 1, 0x0000, 4)
if err != nil {
    return err
}
if err := cli.WriteSingleCoil(ctx, 1, 0x0010, true); err != nil {
    return err
}
```

服务端只需一次调用：

```go
srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
    Addr: "127.0.0.1:5020",
    Device: &gmodbus.DeviceIdentification{VendorName: "ACME", ProductCode: "PLC-1"},
})
if err != nil {
    return err
}
defer srv.Close()
```

`NewServer` 立即开始服务；`Addr()` 返回实际监听地址（串口返回设备路径），测试可用
`127.0.0.1:0`。

## 传输

`Network` 选择传输，零值是 `NetworkTCP`。

| 传输 | 常量 | 分帧 | 客户端选项 | 服务端选项 |
| --- | --- | --- | --- | --- |
| `tcp` | `NetworkTCP` | MBAP over TCP | `Addr` | `Addr`、`Listener` |
| `tls` | `NetworkTLS` | MBAP over TLS | `TLS`（可选） | `TLS`（`Listener` 已终结 TLS 时可不填） |
| `udp` | `NetworkUDP` | 每个数据报一个 MBAP 帧 | `Addr` | `Addr`、`PacketConn` |
| `dtls` | `NetworkDTLS` | 每条 DTLS 记录一个 MBAP 帧 | `DTLS`（必填） | `DTLS`（`Listener` 已终结 DTLS 时可不填） |
| `rtu` | `NetworkRTU` | 串口上的 CRC16 RTU 帧；`Addr` 是设备路径 | `Addr` 或 `Port`、`Serial` | `Addr` 或 `Port`、`Serial` |
| `rtu-tcp` | `NetworkRTUOverTCP` | RTU 帧 over TCP | `Addr`、`RTU` | `Addr`、`Listener`、`RTU` |
| `rtu-udp` | `NetworkRTUOverUDP` | 每个数据报一个 RTU 帧 | `Addr`、`RTU` | `Addr`、`PacketConn`、`RTU` |

`ClientConfig.TLS` 为零时使用系统根证书，并从 `Addr` 推导 `ServerName`；
`ClientConfig.DTLS` 必填，`ServerName` 为空时同样从 `Addr` 推导。服务端的 TLS/DTLS
配置按传入值使用，所有权归调用方。

`RTU` 控制 RTU 分帧变体：零值写入并校验 CRC；`modbus.RTUOptions{NoCRC: true}` 在
`rtu-tcp`/`rtu-udp` 上去掉校验。串口上拒绝 `NoCRC`，规范要求保留校验和。

## 客户端

### 配置

| 字段 | 含义 | 零值 |
| --- | --- | --- |
| `Network` | 传输 | `NetworkTCP` |
| `Addr` | `host:port` 或串口设备路径 | 必填（`Port` 例外） |
| `DialTimeout` | 单次连接与 TLS/DTLS 握手的界限 | `DefaultDialTimeout`（5s） |
| `Timeout` | 单次尝试（写+读）的界限 | `DefaultTimeout`（5s） |
| `Retries` | 首次之外的额外尝试，仅超时/传输失败 | 一次尝试 |
| `MaxPDU` | 入站 PDU 上限 | `modbus.MaxPDU` |
| `Metrics` | 每次尝试的钩子 | 无，且零成本 |
| `Reconnect` | 可选自动恢复策略 | nil，快速失败 |
| `TLS` | `tls` 的 TLS 配置 | 系统根证书 |
| `DTLS` | `dtls` 的 DTLS 配置 | `dtls` 必填 |
| `Serial` | `rtu` 的串口参数 | 19200、8 数据位、1 停止位、无校验 |
| `RTU` | `rtu-tcp`/`rtu-udp` 的 RTU 变体 | 带 CRC |
| `Functions` | 厂商功能码注册表 | 仅内置功能码 |
| `Port` | 已打开的串口（`rtu`） | 用 `Addr` 和 `Serial` 打开 |

`SerialConfig` 包含 `Baud`、`DataBits`、`StopBits`、`Parity`，零值取默认。传入的
`Port` 自行管理线路参数，必须支持 `SetDeadline`，并由 `Client.Close` 关闭。

### 操作

| 功能码 | 方法 | 结果 |
| --- | --- | --- |
| 0x01 读线圈 | `ReadCoils` | `[]bool` |
| 0x02 读离散输入 | `ReadDiscreteInputs` | `[]bool` |
| 0x03 读保持寄存器 | `ReadHoldingRegisters` | `[]uint16` |
| 0x04 读输入寄存器 | `ReadInputRegisters` | `[]uint16` |
| 0x05 写单线圈 | `WriteSingleCoil` | error |
| 0x06 写单寄存器 | `WriteSingleRegister` | error |
| 0x0F 写多线圈 | `WriteMultipleCoils` | error |
| 0x10 写多寄存器 | `WriteMultipleRegisters` | error |
| 0x16 掩码写寄存器 | `MaskWriteRegister` | error |
| 0x17 读写多寄存器 | `ReadWriteMultipleRegisters` | `[]uint16` |
| 0x2B/0x0E 读设备标识 | `ReadDeviceIdentification` | `*modbus.ReadDeviceIdentificationResponse` |
| 任意已注册消息 | `Execute(ctx, unit, req)` | `modbus.Message` |

0x17 先写后读且只占一个事务。设备标识响应可能带 `MoreFollows`/`NextObjectID`，调用方用
相同 read code 加该对象 ID 再发一次请求继续读取。

### 厂商功能码

`ClientConfig.Functions` 与 `ServerConfig.Functions` 共用 gnety 的注册表：

```go
functions := modbus.Functions{
    0x64: {
        DecodeRequest:  func(pdu []byte) (modbus.Message, error) { ... },
        DecodeResponse: func(req modbus.Message, pdu []byte) (modbus.Message, error) { ... },
        // 仅 RTU 串口 / RTU over TCP 需要（没有长度字段）。
        RequestLength:  func(frame []byte) (int, error) { ... },
        ResponseLength: func(req modbus.Message, frame []byte) (int, error) { ... },
    },
}
cli, _ := gmodbus.NewClient(ctx, gmodbus.ClientConfig{Addr: "10.0.0.5:502", Functions: functions})
resp, err := cli.Execute(ctx, 1, &myVendorRequest{...})
```

注册表先于内置解码器工作，因此也能替换标准功能码的载荷。异常响应始终由内置解码器处理，
厂商请求同样能正确收到异常。

### RTU 广播

`rtu`、`rtu-tcp`、`rtu-udp` 上 unit 0 是广播：会改变状态的写方法（0x05、0x06、0x0F、
0x10、0x16）发送后立即返回，服务端生效但不回包；读请求以及带读的 0x17 直接返回错误，
不会上线。MBAP 传输上 unit 0 是普通 unit。

广播没有确认，传输失败后的重试或重连可能把同一次写应用两次；只有设备侧幂等时广播写才
安全。

## 服务端

```go
store := gmodbus.NewMemoryStore(nil) // unit 1，每区 1024 点
unit := store.AddUnit(7, gmodbus.UnitConfig{HoldingRegisters: 256})

srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
    Network: gmodbus.NetworkTCP,
    Addr:    ":502",
    Store:   store,
    Handler: gmodbus.HandlerFunc(func(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
        return nil, gmodbus.ErrUnhandled // 交给内置寄存器区
    }),
    Device: &gmodbus.DeviceIdentification{VendorName: "ACME", ProductCode: "PLC-1", MajorMinorRevision: "1.2"},
})
```

`NewServer` 绑定传输并开始服务。`Close` 停止监听、关闭所有连接和串口；传给 `NewServer`
的 context 取消时同样关闭。

### 分发顺序

1. `Handler` 优先。返回响应就发送；返回 `*modbus.Exception`（错误或消息）就发送该异常；
   返回 `ErrUnhandled` 落到寄存器区；其他错误经 `OnError` 上报并回 `ServerDeviceFailure`。
2. `MemoryStore` 应答标准功能码；越界地址回 `IllegalDataAddress`；未知功能码返回
   `ErrUnhandled`。
3. `DeviceIdentification` 在 Handler 与 Store 都未处理 0x2B/0x0E 时代答。
4. 仍未处理则回 `IllegalFunction`。

Handler 的 panic 会被捕获、经 `OnError` 上报、回 `ServerDeviceFailure`，并记为
`ServerResultError`。传给 Handler 的 context 在请求结束或服务端关闭时取消，因此阻塞的
Handler 不会把 `Close` 拖住。

### MemoryStore

- 每个 unit 独立四区，地址都从 0 开始。
- `AddUnit` 创建或替换一个 unit；替换正在被请求使用的 unit 会丢弃该请求对旧区的写入。
- `UnitConfig` 配置各区大小，零字段取 `DefaultUnitSize`（1024）。请求一个尚不存在的
  unit 时会按默认配置懒创建，因此任意 unit 都可用。
- `UnitStore` 提供 `Coils`/`SetCoils`/`Coil`/`SetCoil`、`HoldingRegisters`/
  `SetHoldingRegisters`、`MaskWrite`、`ReadWrite`，离散输入与输入寄存器同理。
- `OnWrite` 在写成功后回调（值已复制），在锁外执行，因此回调内不能阻塞；广播会为每个 unit 各报告一次。
- 请求一个不存在的 unit 会创建它，因此不断更换 unit id 的对端会撑大存储。面对不可信对端时，
  预先创建要服务的 unit，并在 `Handler` 里拦掉其余请求（或固定默认配置后拒绝未知 unit）。
- 0x17 在同一把 unit 锁内完成写与读，符合该功能码的原子语义。

### 设备标识

`DeviceIdentification` 用自身字符串填充七个标识对象，空字符串不会出现在响应里。超过
`MaxPDU` 的响应会拆分：服务端设置 `MoreFollows` 与 `NextObjectID`，客户端用相同 read code
加该对象 ID 继续。符合级别默认按 read code 推导（0x81 basic、0x82 regular、0x83 extended，
即支持 individual 访问）；`ConformityLevel` 可显式覆盖。

### 服务端配置

| 字段 | 含义 | 零值 |
| --- | --- | --- |
| `Network`、`Addr` | 传输与地址 | `NetworkTCP`；未提供 socket/Port 时地址必填 |
| `TLS`、`DTLS` | 安全配置 | `tls`/`dtls` 必填，除非 `Listener` 已终结安全层 |
| `Listener`、`PacketConn` | 已打开的 socket | nil；服务端负责关闭 |
| `Serial`、`Port`、`RTU` | 串口与 RTU 变体 | 与客户端相同 |
| `Handler`、`Store`、`Device` | 分发栈 | 无 Handler；默认寄存器区；无设备标识 |
| `Functions` | 厂商功能码注册表 | 仅内置功能码 |
| `MaxPDU` | 入站 PDU 上限 | `modbus.MaxPDU` |
| `MaxConnections` | 并发流连接/数据报 peer 上限 | 不限 |
| `IdleTimeout` | 关闭空闲流连接；遗忘空闲数据报 peer（串口不适用） | 关闭；UDP/DTLS 应设置以回收 peer |
| `ShutdownTimeout` | `Close` 的界限 | `DefaultShutdownTimeout`（5s） |
| `Metrics` | 每请求钩子 | 无，且零成本 |
| `OnError` | 传输与 Handler 错误 | 丢弃 |

## 扩展点

消费方要实现的或要提供的东西都在 [`hook`](hook/)：`Handler`（应答或接管服务端请求）、
`Metrics` 与 `ServerMetrics`（可观测性）、`Port`（已打开的串口）。根包把它们全部以类型别名
重新导出，因此两条 import 路径是同一种类型，已有代码不用改：

```go
var h gmodbus.Handler = hook.HandlerFunc(myHandle) // 同一个类型
```

单闭包适配器：`hook.HandlerFunc`、`hook.MetricsFunc`、`hook.ServerMetricsFunc`；
`hook.NoopMetrics`、`hook.NoopServerMetrics` 丢弃所有事件。

handler 返回 `hook.ErrUnhandled` 表示交给服务端回退到 `MemoryStore`；它与
`gmodbus.ErrUnhandled` 是同一个 sentinel，两种写法 `errors.Is` 都能匹配。

要让这些能力在运行期可开关、可观测，把服务端套进插件注册表：
[`gplugin`](https://github.com/maxid/gplugin) 的 `gplugin.NewRegistry(server)` 提供具名插件、
三态生命周期、观察者与单品启停；本包只定义"插件能插到哪里"。

## 行为

### 事务与重试

每次 MBAP 操作取下一个事务号（容忍回绕），事务号或 unit 不匹配的响应被拒绝；RTU 没有
事务号，响应必须匹配 unit 和功能码，CRC 负责拒绝损坏帧。以上都返回
`ErrResponseMismatch`。

一次尝试要么得到答案，要么失败：

- **ok**：解码并返回响应。
- **exception**：`*gmodbus.Error`；连接可继续用，不重试。
- **timeout**：`Timeout` 内没有完整响应。
- **transport**：连接失败（拒绝、重置、关闭，或 codec 拒绝的帧）。
- **mismatch**：响应不回答本请求。

超时或传输失败会结束该次连接的寿命，重试会重新连接。没有 `Reconnect` 时，尝试耗尽或
响应不可用会把客户端彻底关闭，之后快速失败 `ErrClosed`；重新建客户端恢复。数据报传输
会忽略上一笔的迟到响应，继续等自己的。

### 重连

设置 `Reconnect` 后，传输失败或被对端关闭的连接会在原地恢复：丢弃坏连接，按有抖动的
指数退避重拨（`Delay` 起步、逐次翻倍、`MaxDelay` 封顶），`MaxAttempts` 限制单次操作
的重拨次数；成功后重试在途请求。串口按同样方式重开。

整个过程在调用方 context 内、占用单一交换槽；`Close` 立即打断退避或连接。永久 TLS/DTLS
配置错误（`ErrTLSConfig`）直接结束，不消耗预算。

### TLS 与 DTLS

客户端在 `DialTimeout` 内完成握手；服务端按传入的 `*tls.Config`/`*dtls.Config` 使用。
证书校验失败、对端不说该协议或安全告警会包装 `ErrTLSConfig`，重连不会重试；瞬时握手失败
（EOF、重置、超时）按普通传输失败重试。

### 错误

| 错误 | 含义 |
| --- | --- |
| `ErrClosed` | 客户端没有连接：已关闭或尝试耗尽 |
| `ErrTimeout` | 所有尝试都超时 |
| `ErrResponseMismatch` | 响应不回答本请求 |
| `ErrTLSConfig` | 永久 TLS/DTLS 握手失败，重试无益 |
| `ErrUnhandled` | Handler 不接受该请求，回落寄存器区 |
| `ErrOutOfRange` | 寄存器区地址/数量越界 |
| `*gmodbus.Error` | Modbus 异常响应；可 unwrap 到 `modbus.ErrException` |

sentinel 都是包装返回，用 `errors.Is` 判断；异常用 `errors.As` 取出。取消调用方 context
会中止在途交换并返回 `ctx.Err()`，连接随之关闭。

### 并发

客户端可并发使用，但同一时刻只有一个请求在途：所有调用竞争同一个槽，后到的等待当前交换
或自己的 context。服务端每条流连接顺序处理请求；数据报传输经 gnety session 独立处理每个
报文。

### 指标

`Metrics.Request(function, result, duration)` 每次客户端尝试在调用方 goroutine 上调用
一次，`result` 为 `ResultOK`/`ResultException`/`ResultTimeout`/`ResultTransport`/
`ResultMismatch`。codec 拒绝的帧记为 `ResultTransport`，因为连接会被丢弃。

`ServerMetrics.Request(function, result, duration)` 每个服务端请求调用一次，`result` 为
`ServerResultOK`/`ServerResultException`/`ServerResultError`。两个钩子都不带 unit、地址
或值，保持低基数。

## 示例

三个可运行的程序：`server` 提供一台带初值的设备，`read-holding` 在七种传输上读一段寄存器，
`serial-server` 在没有串口设备时验证 RTU 那几条路径。

```sh
go run ./examples/server
go run ./examples/read-holding
go run ./examples/serial-server
```

每个示例回答什么问题、哪些参数值得复制、应该看到什么，见
[`examples/README.zh-CN.md`](examples/README.zh-CN.md)。

## 互操作测试

对“不是本库”的实现的互操作矩阵放在独立的 [`interop`](interop/README.md) 模块里，
因此本模块的依赖图既不携带第三方 Modbus 实现，也不携带容器工具链。默认层是 Go 实现
`github.com/simonvetter/modbus` 的双向测试；集成层是容器里的 pymodbus（Python 参考实现）
双向测试，加上对接伪终端对的 RTU 串口路径：

```sh
cd interop && go test ./...                    # 另一个 Go 实现，不需要 Docker
cd interop && go test -tags=integration ./...  # 容器里的 pymodbus，以及串口路径
```

interop 模块用 `replace` 指向父目录，始终测试工作副本，不单独发布。

## 范围

覆盖客户端与服务端角色及上面的七种传输。明确不做：RS485 半双工回声消除、强制 RTU 3.5
字符静默间隔、串口写超时（串口库没有该设置）、0x07/0x08/0x0B 功能码、寄存器映射/物模型
与命令行工具。

## 文档

- [`docs/ARCHITECTURE.zh-CN.md`](docs/ARCHITECTURE.zh-CN.md)：包地图、三类传输、客户端与
  服务端状态机，以及一个新功能码该落在哪。
- [`docs/DEPLOYMENT.zh-CN.md`](docs/DEPLOYMENT.zh-CN.md)：串口参数、超时与重试预算、网关
  配置，以及现场最先遇到的失败模式。
- [`examples/README.zh-CN.md`](examples/README.zh-CN.md)：每个示例回答什么问题。
- [`interop/README.zh-CN.md`](interop/README.zh-CN.md)：互操作矩阵与每层运行的前提。

```sh
go doc -all .
```

## 许可证

MIT，见 [LICENSE](LICENSE)。
