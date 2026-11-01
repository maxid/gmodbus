[English](README.md) | 简体中文

# interop

针对独立 Modbus 实现（[`simonvetter/modbus`](https://github.com/simonvetter/modbus)）的互操作测试。

测试放在独立的 Go module 里，因此本库的依赖图不携带第三方 Modbus 依赖。该模块用
`replace` 把 `github.com/maxid/gmodbus` 指向父目录，始终测试工作副本，只用于检出仓库后
运行，不单独发布：

```sh
cd interop && go test ./...
```

CI 单独运行该模块。父模块更新依赖 pin 后，在这里刷新校验和：

```sh
cd interop && go mod tidy
```
