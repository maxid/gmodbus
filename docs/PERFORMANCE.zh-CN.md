[English](PERFORMANCE.md) | 简体中文

# 性能基线

本文记录 [`bench_test.go`](../bench_test.go) 里那组基准测了什么，以及当前代码树在一台机器上
的花费。它只回答一个窄问题——“这个库会不会成为瓶颈？”——刻意不回答“一个进程能轮询多少台
设备”。边界见[这些数字的边界](#这些数字的边界)。

换了版本、机器或 Go 工具链就重新测：这些是指示值，不是容量承诺。

## 环境

| 项 | 值 |
|---|---|
| 日期 | 2026-11-04 |
| Go | `go1.27.1` |
| `GOOS` / `GOARCH` | `darwin` / `arm64` |
| CPU | Apple M1 Max，10 核（`GOMAXPROCS=10`） |
| 内存 | 64 GiB |
| 操作系统 | macOS 14.8.9（Darwin 23J631） |
| 服务端 | 进程内模拟从站（`MemoryStore`），不是 PLC |
| 传输 | 回环 TCP（`127.0.0.1`），单连接，单请求在途 |

## 方法

两种不同的花费，分开测：

| 基准 | 测什么 | 拓扑 |
|---|---|---|
| `BenchmarkReadHolding/registers=4` | 一次 `ReadHoldingRegisters` 端到端 | 客户端 → 回环 TCP → 进程内服务端 → `MemoryStore` → 返回 |
| `BenchmarkReadHolding/registers=125` | 同样的调用，携带协议允许的最大量 | 同上；应答是 255 字节帧 |
| `BenchmarkWriteSingleRegister` | 一次写端到端 | 同上；一个往返，PDU 更小 |
| `BenchmarkRTUFrame/encode` | PDU 编码加 CRC 帧 RTU 打包，无 socket | 无 |
| `BenchmarkRTUFrame/decode` | 一帧 CRC RTU 应答解码成类型化消息，无 socket | 每帧复用一个 `bufio.Reader`，与连接一致 |

- 服务端是真的：`NewServer` 绑一个回环 socket，用内置寄存器区应答。没有任何 mock，客户端走的
  就是部署时的代码路径。
- 建连、播种与一次预热交换都在 `b.ResetTimer()` 之前，所以 `ns/op` 是稳态交换，不是拨号。
- 因此 `B/op` 与 `allocs/op` 覆盖**两侧**：客户端与它的服务端都在基准进程里。它们是“一次交换”
  的花费，不是“一次库调用”。
- `BenchmarkRTUFrame` 只测分帧算术：这是库在串口线路上每帧要做的事，而线路比它慢一万倍。

## 结果

上面那台机器，`-count=5`，取中位数。

| 基准 | `ns/op` | `B/op` | `allocs/op` |
|---|---|---|---|
| `BenchmarkReadHolding/registers=4` | 23 400 | 1 080 | 48 |
| `BenchmarkReadHolding/registers=125` | 25 400 | 2 752 | 169 |
| `BenchmarkWriteSingleRegister` | 22 000 | 992 | 45 |
| `BenchmarkRTUFrame/encode` | 133 | 136 | 6 |
| `BenchmarkRTUFrame/decode` | 268 | 336 | 4 |

同一张表的两种读法：

- **回环往返是 TCP 部署的下限。** 客户端与服务端同机、网络是内核回环时，一次交换约 23 µs。
  真实网络在此之上再加自己的往返时间；库占的那部分是分配列，不是微秒列。
- **串口线路上，线就是一切。** 19200 8N1 下，一次交换——8 字节请求与 13 字节应答——占用线路
  约 11 ms；9600 下约 22 ms。编码请求 133 ns、解码应答 268 ns，库只花掉总线时间的约 0.004%。
  再优化分帧，部署侧看不出来。

## 复现

```sh
cd libs/gmodbus
go test -run '^$' -bench 'BenchmarkReadHolding|BenchmarkWriteSingleRegister|BenchmarkRTUFrame' -benchmem -count=5 .
```

想看到串口线路的真实画面需要真实端口；手边没设备时 `examples/serial-server` 用伪终端对跑同一
份代码。

## 这些数字的边界

- **从站是模拟的。** `MemoryStore` 从内存应答；真实 PLC 会加上设备转向时间，而串口总线上它
  压倒这里的一切。
- **单连接、单请求在途。** 本库按设计把调用方串行在一条线路上，所以带并发的基准测的是队列，
  不是协议。
- **没有 TLS 与 DTLS。** 握手每连接一次，记录层给每帧加工作；真在意就用部署自己的证书去测。
- **一台机器。** 服务端与客户端、与基准框架共享 CPU。
- **分配未调优。** 一次四寄存器读的 48 次分配，是当前“一次交换含两侧”的花费；更低的数字会
  是一个受欢迎的 PR，而不是一个承诺。

## 真实设备

这里没有任何针对真实硬件的测量：那需要有人在台子上放一台 PLC 或 RTU。如果你测了，按下面的
形状记在这里，并附上库的版本：

```
| 日期 | 设备 | 传输 | 波特率 | 超时 | 交换中位数 | 备注 |
|------|------|------|--------|------|------------|------|
```

值得记录的数字是**交换时延**（请求发出到应答解码完成），测法同 `BenchmarkReadHolding`：热连接、
单请求在途、超时取自设备自身的响应时间。上一节的线路时间是它打不破的下限。
