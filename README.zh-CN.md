[English](README.md) | 简体中文

# gmodbus

Go 的 **Modbus TCP master**，构建在
[gnety](https://github.com/maxid/gnety) 的类型化 Modbus 编解码器
（`codec/modbus`）之上。

> 状态：**开发快照（未打标签）** — `0.0.0-dev`。线格式
> 来自编解码器；本库拥有连接、事务、
> 重试、异常映射与轮询调度。

## 特性

- 每个客户端一条 TCP 连接，为平台使用的功能码提供类型化操作：
  读线圈、离散输入、保持寄存器与输入寄存器；写单个与多个
  线圈及寄存器。
- 自动的事务 id 与严格匹配：事务 id
  或 unit id 与请求不匹配的响应会被拒绝。
- `Timeout` 限制每次尝试，`Retries` 限制超时与传输层重试，
  而 Modbus 异常响应是一个已送达的答案：它变为类型化的
  `*gmodbus.Error`，且永远不会被重试。
- 可选的自动重连：在传输层故障或对端关闭连接之后，
  客户端以有界、带抖动的指数退避重新拨号，并重试在途请求。
  没有该策略时保持快速失败的 `ErrClosed` 行为。
- 可插拔传输：TLS（握手受 `DialTimeout` 约束），或自定义
  `Dialer`，用于 TLS、代理、测试替身或任何其他 `net.Conn`
  来源。
- 每个客户端同时只有一个在途请求。并发调用方在链路上串行化，
  并等待当前交换完成或等待自己的 context。
- 低基数指标钩子（仅功能码与结果），并带有 nil 钩子的
  快速路径。
- 轮询运行器，让各独立任务按自己的调度运行，
  并上报其错误而不中断。

## 安装

```sh
go get github.com/maxid/gmodbus@<commit>
```

开发期间不发布标签；请使用 commit 伪版本（pseudo-version）。

## 快速开始

```go
cli, err := gmodbus.Dial(ctx, gmodbus.Config{
    Addr:    "10.0.0.5:502",
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

## 示例

[`examples/read-holding`](examples/read-holding/main.go) 从 `-addr` 指定的设备读取保持寄存器并打印。
没有默认主机，因此在没有可达设备时示例会带着传输错误以非零状态退出，而不是 panic：

```sh
go run ./examples/read-holding -addr 192.0.2.10:502
go run ./examples/read-holding -addr 127.0.0.1:5020 -unit 1 -start 0 -quantity 4
```

## 异常

拒绝请求的服务端会以异常响应作答。客户端将其映射为
`*gmodbus.Error`，其中携带 unit id、请求功能
码与异常码，并可解包到编解码器的
`modbus.ErrException`：

```go
_, err := cli.ReadHoldingRegisters(ctx, 1, 0x9000, 1)
var rejected *gmodbus.Error
switch {
case errors.As(err, &rejected):
    // rejected.Code is modbus.ExceptionIllegalDataAddress here
case errors.Is(err, modbus.ErrException):
    // any exception code
}
```

## 轮询

```go
tasks := []gmodbus.Task{
    {
        Name:  "temperature",
        Every: time.Second,
        Do: func(ctx context.Context) error {
            values, err := cli.ReadInputRegisters(ctx, 1, 0x0000, 1)
            if err != nil {
                return err
            }
            latest.Store(values[0])
            return nil
        },
    },
    {
        Name:  "setpoint",
        Every: 5 * time.Second,
        Do: func(ctx context.Context) error {
            return cli.WriteSingleRegister(ctx, 1, 0x0010, want.Load())
        },
    },
}

err := cli.Poll(ctx, tasks, func(task gmodbus.Task, err error) {
    log.Printf("poll %s: %v", task.Name, err)
})
```

`Poll` 会校验任务，立即运行每个任务，之后每隔 `Every` 运行一次
（在各自的 goroutine 上，绝不与自身重叠），通过回调
上报失败，并且只在 context 结束时返回。单个失败的任务绝不会
停止循环或其他任务。任务名必须非空且唯一，`Every`
不得短于 `MinTaskInterval`（10ms）。

长期运行的轮询器应设置 `Reconnect`，这样丢失的连接会
就地恢复，而不是让之后每个任务都以 `ErrClosed` 失败：

```go
cli, err := gmodbus.Dial(ctx, gmodbus.Config{
    Addr: "10.0.0.5:502",
    Reconnect: &gmodbus.ReconnectPolicy{
        Delay:       100 * time.Millisecond,
        MaxDelay:    5 * time.Second,
        MaxAttempts: 5,
    },
})
```

## 行为

### 配置

| 字段 | 含义 | 零值 |
| --- | --- | --- |
| `Addr` | 服务端地址，`host:port` | 必填 |
| `DialTimeout` | 一次 TCP 连接的上限 | `DefaultDialTimeout`（5s） |
| `Timeout` | 一次尝试的上限，写加读 | `DefaultTimeout`（5s） |
| `Retries` | 首次之后的额外尝试次数，仅限超时/传输层 | 只尝试一次 |
| `MaxPDU` | 入站 PDU 上限 | `modbus.MaxPDU` |
| `Metrics` | 每次尝试的钩子 | 无，且不产生任何开销 |
| `Reconnect` | 可选启用的恢复策略：延迟、最大延迟与拨号预算 | nil，快速失败 |
| `TLS` | 内置拨号器的 TLS 配置 | 明文 TCP |
| `Dialer` | 自定义传输拨号器，优先于 `TLS` | 内置 TCP 拨号器 |

负的时长、负的 `Retries`、负的重连字段、
重连 `MaxDelay` 小于 `Delay`，以及 `MaxPDU` 大于 `modbus.MaxPDU`，
都会被 `Dial` 拒绝。

### 事务与重试

每个操作都从每客户端计数器取下一个事务 id
（容忍回绕），写入帧，并通过编解码器按其必须应答的请求
读取回复。事务 id 或 unit
id 不同的回复即为 `ErrTransactionMismatch`。

一次尝试要么得到答案，要么失败：

- **ok** — 响应被解码并返回。
- **exception** — 一个 `*gmodbus.Error`；连接保持可用，且不会
  发生重试。
- **timeout** — 在 `Timeout` 内没有完整响应。
- **transport** — 连接失败（被拒绝、被重置、被关闭，或编解码器
  拒绝了帧）。
- **mismatch** — 回复没有应答此请求。

超时或传输层故障会终止该次尝试的连接，因此重试会拨一条
新连接。在没有 `Reconnect` 时、尝试次数耗尽时，或回复
不可用（不匹配，或编解码器拒绝的帧）时，客户端会永久关闭
其连接：之后每次调用都以 `ErrClosed` 快速失败，而不是
在一条可能仍携带过期回复的流上显得健康。请拨号一个新
客户端来恢复。

### 重连

设置 `Reconnect` 可将传输层故障或对端关闭的连接
转为就地恢复。客户端丢弃损坏的连接，并以带抖动的指数退避
重新拨号：第一次重拨使用 `Delay`，每次尝试翻倍
并以 `MaxDelay` 为上限。`MaxAttempts` 限制单个
操作可消耗的拨号次数。一旦重拨成功，在途请求会
以相同的事务 id 重试，因此轮询器可以挺过短暂中断而
不看到错误。

整个序列都在调用方的 context 内运行，并占用唯一的
交换槽位，因此不会新增 goroutine，也不会并行拨号。`Close` 会
立即中断退避等待或拨号。预算耗尽时客户端上报
`ErrClosed` 并保持关闭；`Close` 显式做同样的事。`Retries` 仍然
限制超时：超时不会启动重连，但在有 `Reconnect`
策略时，超时尝试耗尽的客户端对下一次调用仍然可用。

### TLS 与自定义拨号器

`TLS` 包装内置的 TCP 连接，并在
`DialTimeout` 内完成握手。当重试无济于事时（证书
校验、对端不提供 TLS、TLS alert），握手失败会包装
`ErrTLSConfig`；当失败是暂时性的（超时、重置、提前
EOF）时包装 `ErrTLSHandshake`。重连不会在 `ErrTLSConfig` 上
消耗尝试次数。

`Dialer` 优先于 `TLS`：设置之后它拥有整个传输，
接收 `"tcp"` 与 `Addr`，并可用
`tls.DialWithDialer` 或 `tls.Client` 自行完成 TLS。允许同时提供 `TLS` 配置
与自定义拨号器，而 `TLS` 配置并不会被使用。`Dialer` 必须遵守其 context；`Close`
会取消它。

### 错误

| 错误 | 含义 |
| --- | --- |
| `ErrClosed` | 客户端没有连接：已关闭，或尝试次数耗尽 |
| `ErrTimeout` | 每次尝试都超时 |
| `ErrTransactionMismatch` | 回复未应答该请求 |
| `ErrTLSConfig` | 永久性的 TLS 握手失败；重试无法修复 |
| `ErrTLSHandshake` | 暂时性的 TLS 握手失败；重连可以重试 |
| `*gmodbus.Error` | Modbus 异常响应；可解包为 `modbus.ErrException` |

哨兵错误是被包装的，因此用 `errors.Is` 判断；用 `errors.As`
取出类型化异常。取消调用方的 context 会以 `ctx.Err()` 中止挂起的
交换并关闭连接，因为被放弃的回复否则会在之后的某个请求
期间到达。

### 并发

客户端可安全并发使用，但只保持一个在途请求：每次
调用都占用同一个槽位，第二个调用方会等待当前交换或
其自身 context，先到者为准。Modbus TCP 在此没有乱序
保证，因此重叠交换被有意设为不可能。

### 指标

`Metrics.Request(function, result, duration)` 每次尝试运行一次，在
调用方的 goroutine 上。`result` 为 `ResultOK`、`ResultException`、
`ResultTimeout`、`ResultTransport` 或 `ResultMismatch` 之一；编解码器拒绝的帧
报告为 `ResultTransport`，因为连接被丢弃。按设计没有
unit、地址或值的标签。

## 范围

仅支持 Modbus **TCP master**。有意排除的范围：串口 RTU 分帧与
串口参数、slave 与 server 角色、寄存器映射、设备模型以及
命令行工具。MBAP 头与 PDU 编码位于
`github.com/maxid/gnety/codec/modbus`，此处永不重新实现。

## 文档

```sh
go doc -all .
```

## 许可证

MIT。见 [LICENSE](LICENSE)。
