English | [简体中文](README.zh-CN.md)

# interop

The interoperability matrix: this library against implementations that are not
this library. Two layers, and only the second needs Docker.

## The default layer: another Go implementation

`interop_test.go` drives [`simonvetter/modbus`](https://github.com/simonvetter/modbus)
in both directions, in process, on loopback: its server for our client, its
client against our server over TCP, UDP, RTU over TCP and RTU over UDP. It needs
nothing but `go test`:

```sh
cd interop && go test ./...
```

## The integration layer: the reference Python stack and a real device path

Two files behind `//go:build integration`:

- `pymodbus_test.go` — [pymodbus](https://github.com/pymodbus-dev/pymodbus), the
  reference Python Modbus stack, in a container:
  - **direction A**: our client reads and writes a pymodbus slave built from
    [`docker/pymodbus/slave.py`](docker/pymodbus/slave.py);
  - **direction B**: pymodbus's own client, run from the same image, reads and
    writes our server through `host.docker.internal`.
- `rtu_pty_test.go` — RTU over a real serial device path with no hardware: two
  pseudo-terminal pairs bridged master to master stand in for two RS-485 nodes
  and the cable between them, and both ends open their slave path with the serial
  transport, so the device open, the line parameters and the CRC framing are the
  real code.

```sh
cd interop && go test -tags=integration ./...
```

Every container case skips with the reason when no Docker daemon answers, and the
image is built from `docker/pymodbus/` on first use (pinned to the pymodbus
release the scripts were written against). The serial case needs only
`/dev/ptmx`, which Linux and macOS both provide.

## Why a separate module

The tests live in their own Go module so the library's module graph carries no
third-party Modbus implementation and no container toolkit. The module replaces
`github.com/maxid/gmodbus` with the parent directory, so it always exercises the
working copy and is never published on its own. When the parent module changes
its dependency pins, refresh the sums here:

```sh
cd interop && go mod tidy
```
