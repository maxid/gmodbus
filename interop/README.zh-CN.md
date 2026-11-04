[English](README.md) | 简体中文

# interop

互操作矩阵：本库对“不是本库”的实现。分两层，只有第二层需要 Docker。

## 默认层：另一个 Go 实现

`interop_test.go` 双向驱动 [`simonvetter/modbus`](https://github.com/simonvetter/modbus)，
进程内、回环上跑：它的服务端对我们客户端，它的客户端对我们服务端，覆盖 TCP、UDP、
RTU over TCP 与 RTU over UDP。只需要 `go test`：

```sh
cd interop && go test ./...
```

## 集成层：参考实现（Python）与真实设备路径

两个文件带 `//go:build integration`：

- `pymodbus_test.go`：把参考实现 [pymodbus](https://github.com/pymodbus-dev/pymodbus)
  放进容器。
  - **方向 A**：我们的客户端读写 [`docker/pymodbus/slave.py`](docker/pymodbus/slave.py)
    起的 pymodbus 从站；
  - **方向 B**：同一个镜像里的 pymodbus 客户端，经 `host.docker.internal` 读写我们的服务端。
- `rtu_pty_test.go`：没有硬件时的真实串口路径。两对伪终端在 master 侧对接，充当两个 485
  节点和它们之间的电缆；两端都用串口传输打开自己的 slave 设备路径，因此设备打开、线路参数
  和 CRC 分帧都是真代码。

```sh
cd interop && go test -tags=integration ./...
```

Docker 守护进程不可用时，每个容器用例都会带上原因 skip；镜像在首次使用时从
`docker/pymodbus/` 构建（pin 在脚本编写时的 pymodbus 版本）。串口用例只需要 `/dev/ptmx`，
Linux 与 macOS 都有。

## 为什么单独一个 module

测试放在独立的 Go module 里，因此本库的依赖图不携带第三方 Modbus 实现、也不携带容器工具链。
该模块用 `replace` 把 `github.com/maxid/gmodbus` 指向父目录，始终测试工作副本，不单独发布。
父模块更新依赖 pin 后，在这里刷新校验和：

```sh
cd interop && go mod tidy
```
