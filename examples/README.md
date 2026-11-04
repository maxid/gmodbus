English | [简体中文](README.zh-CN.md)

# Examples

Three runnable programs, each answering one question. Every one is a
self-contained `main` package, so `go run ./examples/<name>` from the repository
root is enough — no device, no configuration file, no Docker.

- [server](#server) — how little code serves a working Modbus device, and what a
  write looks like from the server's side.
- [read-holding](#read-holding) — how to read a block of registers from any
  device over any of the seven transports.
- [serial-server](#serial-server) — how to exercise the RTU paths with no serial
  device attached.

## Common conventions

- `-h` prints what the example answers plus commands you can copy verbatim.
- Each demo prints one line per step — `[n/N] action … result` — and `[done] …`
  when it finishes. `server` is the exception: it is a long-running device and
  prints `[serve] …` lines instead.
- Failures go to stderr as `<example>: <action>: <error>` with a non-zero exit
  code. The prefix is the example's directory name.
- Ctrl-C stops any example. A scripted demo prints `[stop] interrupted`, `server`
  prints `[stop] server stopped`, and both exit 0 — an example is not a service.

## server

**Answers:** what a Modbus device looks like from the inside, and what the other
examples talk to. It serves a seeded in-memory register map (holding registers
0..3 hold `10 20 30 40`, input registers 0..3 hold `100 200 300 400`) through the
built-in `MemoryStore`, and logs every write through `MemoryStore.OnWrite`. Any
real master — modpoll, pymodbus, a PLC's engineering tool — can drive it.

```sh
go run ./examples/server
go run ./examples/server -addr 127.0.0.1:0 -unit 7     # announce a free port
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

**Answers:** "is this device answering?" It reads one block of holding registers
(function code 0x03) with the transport chosen by `-network`, and prints the
values in decimal and hex. A device that rejects the read is reported with its
Modbus exception code; a device that stays silent is reported as a transport
error, and Ctrl-C during a slow attempt exits 0.

```sh
go run ./examples/server &                                              # a device to read
go run ./examples/read-holding
go run ./examples/read-holding -addr 192.0.2.10:502 -start 0 -quantity 4 -unit 1
go run ./examples/read-holding -network rtu -addr /dev/ttyUSB0 -baud 9600
go run ./examples/read-holding -network udp -addr 192.0.2.10:502 -timeout 2s
```

`-addr` defaults to `127.0.0.1:5020`, the address `server` listens on, so the
first two lines above need nothing else.

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

**Answers:** "how do I test RTU without a serial device?" It runs one RTU slave
and one RTU master against each other and verifies three round trips: a holding
register write and read-back, a coil block, and a read-only input block. The
slave uses unit 7 so the unit id does something visible.

Two links are available:

- `-transport rtu-tcp` (the default) carries the RTU frames — unit id, PDU and
  CRC16 — over loopback TCP. Framing, checksum and response matching are the
  real ones; only the wire is a socket instead of a cable.
- `-transport pty` asks [socat](https://linux.die.net/man/1/socat) for a
  pseudo-terminal pair and runs the slave on one end and the master on the other
  with `NetworkRTU`, so the device path, the line parameters and the serial port
  code are real as well. This is the closest thing to a physical 485 bus without
  hardware.

```sh
go run ./examples/serial-server                         # RTU framing over loopback TCP
go run ./examples/serial-server -transport pty          # a pseudo-terminal pair (needs socat)
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

With `-transport pty` step 1 names the two device paths socat created
(`/dev/ttysNNN`) and step 3 the one the slave holds. To point your own program at
the same pair, ask socat yourself and keep it running:

```sh
socat -d -d pty,raw,echo=0 pty,raw,echo=0     # prints two device paths on stderr
go run ./examples/read-holding -network rtu -addr /dev/ttys011   # the far end
```

`raw` and `echo=0` matter: a line discipline that echoes would send every frame
straight back and look like a second answer.

## What the examples do not cover

TLS and DTLS need a certificate pair and a peer that speaks them — the library
tests generate both, and `read-holding -cacert` is the flag to point at a real
one. Broadcast (unit 0) is a write with no reply and belongs to the client
documentation rather than a demo. Polling, reconnection and the metrics hooks are
covered by the library tests and the README.
