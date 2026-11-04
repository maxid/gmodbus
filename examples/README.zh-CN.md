[English](README.md) | 简体中文

# 示例

三个可运行的程序，各自回答一个问题。每个都是自包含的 `main` 包，在仓库根目录
`go run ./examples/<name>` 就能跑——不需要设备、不需要配置文件、不需要 Docker。

- [server](#server)：一个能用的 Modbus 设备最少要写多少代码，以及写入在服务端是什么样子。
- [read-holding](#read-holding)：怎样在七种传输上从任意设备读一段寄存器。
- [serial-server](#serial-server)：手边没有串口设备时，怎样验证 RTU 那几条路径。

## 共同约定

- `-h` 打印这个示例回答什么问题，以及可以直接复制的命令。
- 每个演示逐步打印 `[n/N] 动作 … 结果`，结束打 `[done] …`。`server` 是例外：它是长期
  运行的设备，打的是 `[serve] …`。
- 失败写到 stderr，格式 `<示例名>: <动作>: <错误>`，退出码非 0。前缀就是示例的目录名。
- Ctrl-C 停任何示例。脚本式演示打 `[stop] interrupted`，`server` 打 `[stop] server stopped`，
  两者退出码都是 0——示例不是服务。

## server

**回答什么：** Modbus 设备内部长什么样，以及别的示例在对谁说话。它用内置的 `MemoryStore`
提供一块写死初值的寄存器区（保持寄存器 0..3 是 `10 20 30 40`，输入寄存器 0..3 是
`100 200 300 400`），并通过 `MemoryStore.OnWrite` 打印每一次写入。任何真实主站——modpoll、
pymodbus、PLC 的组态工具——都能把它当成设备来读写。

```sh
go run ./examples/server
go run ./examples/server -addr 127.0.0.1:0 -unit 7     # 自动挑一个空闲端口并打印
go run ./examples/server -h
```

```
[serve] Modbus TCP server on 127.0.0.1:5020, unit 1
[serve] holding registers 0..3 = [10 20 30 40], input registers 0..3 = [100 200 300 400]
[serve] every write is logged; Ctrl-C stops the server
2026/11/04 10:01:48 write unit 1 registers at 2: [4660]
[stop] server stopped
```

## read-holding

**回答什么：**“这台设备到底答不答？”它按 `-network` 选传输，读一段保持寄存器
（功能码 0x03），把值以十进制和十六进制打出来。设备拒绝时打印它的 Modbus 异常码；
设备不吭声时按传输错误报错；慢设备上一次读取期间按 Ctrl-C 退出码是 0。

```sh
go run ./examples/server &                                              # 先有一个设备可读
go run ./examples/read-holding
go run ./examples/read-holding -addr 192.0.2.10:502 -start 0 -quantity 4 -unit 1
go run ./examples/read-holding -network rtu -addr /dev/ttyUSB0 -baud 9600
go run ./examples/read-holding -network udp -addr 192.0.2.10:502 -timeout 2s
```

`-addr` 默认是 `127.0.0.1:5020`，也就是 `server` 监听的地址，所以上面前两行不需要别的参数。

```
[1/3] connecting to 127.0.0.1:5020 over tcp
[2/3] reading 4 holding registers from address 0 (unit 1)
[3/3] holding registers from 127.0.0.1:5020 (unit 1):
  [0x0000] = 10 (0x000A)
  [0x0001] = 20 (0x0014)
  [0x0002] = 30 (0x001E)
  [0x0003] = 40 (0x0028)
[done] read holding registers; connection closed
```

## serial-server

**回答什么：**“没有串口设备怎么验 RTU？”它让一个 RTU 从站和一个 RTU 主站互相对打，验证
三组往返：单个保持寄存器写入并读回、一组线圈、一段只读输入寄存器。从站用单元号 7，
这样单元号在输出里看得见。

两种链路：

- `-transport rtu-tcp`（默认）把 RTU 帧——单元号、PDU、CRC16——走回环 TCP。分帧、校验和、
  应答匹配都是真的，只有“线”是 socket 而不是电缆。
- `-transport pty` 让 [socat](https://linux.die.net/man/1/socat) 建一对伪终端，从站开一端、
  主站开另一端，传输是 `NetworkRTU`，于是设备路径、串口参数和串口代码也都是真的。这是
  没有硬件时最接近一条真实 485 总线的做法。

```sh
go run ./examples/serial-server                         # RTU 帧走回环 TCP
go run ./examples/serial-server -transport pty          # 伪终端对（需要 socat）
go run ./examples/serial-server -transport pty -baud 9600
```

```
[1/6] link: RTU frames over loopback TCP, no serial device involved
[2/6] slave unit 7 seeded: holding 0..3 = [10 20 30 40], input 0..3 = [100 200 300 400]
[3/6] slave listening on 127.0.0.1:52057 (unit 7, CRC16 enforced)
[4/6] master wrote holding[2] = 0x1234 and read it back
[5/6] master wrote coils 0..2 = [true false true] and read them back
[6/6] master read input registers 0..3 = [100 200 300 400]
[done] RTU round trips verified; slave stopped
```

用 `-transport pty` 时，第 1 步会打印 socat 建出的两个设备路径（`/dev/ttysNNN`），第 3 步
打印从站占用的那个。想让自己写的程序接同一对伪终端，自己起一次 socat 并让它一直跑：

```sh
socat -d -d pty,raw,echo=0 pty,raw,echo=0     # 在 stderr 上打印两个设备路径
go run ./examples/read-holding -network rtu -addr /dev/ttys011   # 接另一端
```

`raw` 与 `echo=0` 不能省：行规程一旦回显，每一帧都会被原样送回，看起来就像设备答了第二次。

## 示例没有覆盖的

TLS 与 DTLS 需要一对证书和一个会说这两者的对端——库的测试里两者都会生成，`read-holding
-cacert` 是指向真实证书的开关。广播（单元号 0）是“写了不等回复”，属于客户端文档而不是演示。
轮询、重连与指标钩子由库的测试和 README 覆盖。
