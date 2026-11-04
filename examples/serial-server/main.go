// Command serial-server answers "how do I test Modbus RTU without a serial
// device?". It runs one RTU slave and one RTU master against each other and
// verifies the round trips, with nothing plugged in.
//
// Two links are available:
//
//   - rtu-tcp (the default) carries the RTU frames — unit id, PDU, CRC16 — over a
//     loopback TCP connection. Framing, checksum and response matching are the
//     real ones; only the wire is a socket instead of a serial line.
//   - pty asks socat for a pseudo-terminal pair and runs the slave on one end and
//     the master on the other with the serial transport, so the device path, the
//     line parameters and the serial port code are the real ones too.
//
// The slave uses unit 7 so the unit id does something visible, and it seeds its
// register map before the master starts.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/maxid/gmodbus"
)

// The demo's fixed shape: one unit, six steps, and a bounded wait for every
// exchange so a hung link fails instead of hanging.
const (
	unit  = 7
	steps = 6
)

func main() {
	transport := flag.String("transport", "rtu-tcp", "how the two RTU ends are linked: rtu-tcp (loopback TCP) or pty (a socat pseudo-terminal pair)")
	addr := flag.String("addr", "127.0.0.1:0", "listen address for -transport rtu-tcp")
	baud := flag.Int("baud", 19200, "line speed configured for -transport pty")
	socat := flag.String("socat", "socat", "the socat binary -transport pty runs")
	timeout := flag.Duration("timeout", 3*time.Second, "deadline for one exchange")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `serial-server runs a Modbus RTU slave and a Modbus RTU master against each
other, so the RTU paths can be exercised without a PLC or a serial device.

With -transport rtu-tcp (the default) the RTU frames travel over loopback TCP:
the unit id, the PDU and the CRC16 are the real ones, the wire is a socket.
With -transport pty socat creates a pseudo-terminal pair, the slave opens one
end and the master the other with the serial transport, so the device path and
the line parameters are real as well. It needs socat on PATH.

At the end of either run the master wrote one holding register and one coil
block, and read the registers, the coils and a read-only input block back.

Examples:
  go run ./examples/serial-server
  go run ./examples/serial-server -transport pty
  go run ./examples/serial-server -transport pty -baud 9600
  go run ./examples/serial-server -transport rtu-tcp -timeout 5s

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	switch *transport {
	case "rtu-tcp", "pty":
	default:
		log.Fatalf("serial-server: -transport %q is neither rtu-tcp nor pty", *transport)
	}
	if *baud <= 0 {
		log.Fatalf("serial-server: -baud %d is not a positive line speed", *baud)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, *transport, *addr, *baud, *socat, *timeout); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			fmt.Println("[stop] interrupted")
			return
		}
		log.Fatalf("serial-server: %v", err)
	}
}

// run brings up the link, the slave and the master, and walks the six steps.
func run(ctx context.Context, transport, addr string, baud int, socat string, timeout time.Duration) error {
	// Step 1: the link. Everything the library needs to hear about it comes from
	// here: an address to dial, or two device paths and one cleanup.
	slaveAddr, masterAddr, summary, cleanup, err := link(ctx, transport, addr, baud, socat)
	if err != nil {
		return fmt.Errorf("open the %s link: %w", transport, err)
	}
	defer cleanup()
	fmt.Printf("[1/%d] %s\n", steps, summary)

	// Step 2: the slave's data model. The banks are independent per unit, and a
	// zero UnitConfig field would size that bank to DefaultUnitSize instead.
	store := gmodbus.NewMemoryStore(nil)
	seeded := store.AddUnit(unit, gmodbus.UnitConfig{Coils: 16, DiscreteInputs: 16, HoldingRegisters: 16, InputRegisters: 16})
	if err := seeded.SetHoldingRegisters(0, []uint16{10, 20, 30, 40}); err != nil {
		return fmt.Errorf("seed holding registers: %w", err)
	}
	if err := seeded.SetInputRegisters(0, []uint16{100, 200, 300, 400}); err != nil {
		return fmt.Errorf("seed input registers: %w", err)
	}
	fmt.Printf("[2/%d] slave unit %d seeded: holding 0..3 = [10 20 30 40], input 0..3 = [100 200 300 400]\n", steps, unit)

	// Step 3: the slave. On the serial link it owns the device path, so it has to
	// be listening before the master opens the other end.
	srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
		Network: transportNetwork(transport),
		Addr:    slaveAddr,
		Serial:  serialConfig(transport, baud),
		Store:   store,
		Device:  &gmodbus.DeviceIdentification{VendorName: "gmodbus examples", ProductCode: "SERIAL-1"},
		OnError: func(err error) { log.Printf("serial-server: slave: %v", err) },
	})
	if err != nil {
		return fmt.Errorf("start the slave on %s: %w", slaveAddr, err)
	}
	defer func() { _ = srv.Close() }()
	fmt.Printf("[3/%d] slave listening on %s (unit %d, CRC16 enforced)\n", steps, srv.Addr(), unit)

	// The master dials the address the slave actually bound (the rtu-tcp run asks
	// for port 0) or the other end of the pseudo-terminal pair.
	dial := masterAddr
	if dial == "" {
		dial = srv.Addr()
	}

	// The master is one client with a bounded deadline per attempt.
	cli, err := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
		Network: transportNetwork(transport),
		Addr:    dial,
		Serial:  serialConfig(transport, baud),
		Timeout: timeout,
	})
	if err != nil {
		return fmt.Errorf("dial the slave at %s: %w", dial, err)
	}
	defer func() { _ = cli.Close() }()

	// Step 4: write one register and read it back — function code 0x06 then 0x03.
	writeCtx, cancelWrite := context.WithTimeout(ctx, timeout)
	defer cancelWrite()
	if err := cli.WriteSingleRegister(writeCtx, unit, 2, 0x1234); err != nil {
		return fmt.Errorf("write holding register 2: %w", err)
	}
	registers, err := cli.ReadHoldingRegisters(writeCtx, unit, 2, 1)
	if err != nil {
		return fmt.Errorf("read holding register 2 back: %w", err)
	}
	if len(registers) != 1 || registers[0] != 0x1234 {
		return fmt.Errorf("holding register 2 reads back %v, want [0x1234]", registers)
	}
	fmt.Printf("[4/%d] master wrote holding[2] = 0x1234 and read it back\n", steps)

	// Step 5: a coil block — function code 0x0F then 0x01.
	coilCtx, cancelCoils := context.WithTimeout(ctx, timeout)
	defer cancelCoils()
	want := []bool{true, false, true}
	if err := cli.WriteMultipleCoils(coilCtx, unit, 0, want); err != nil {
		return fmt.Errorf("write coils 0..2: %w", err)
	}
	coils, err := cli.ReadCoils(coilCtx, unit, 0, uint16(len(want)))
	if err != nil {
		return fmt.Errorf("read coils 0..2 back: %w", err)
	}
	if len(coils) != len(want) || coils[0] != want[0] || coils[1] != want[1] || coils[2] != want[2] {
		return fmt.Errorf("coils 0..2 read back %v, want %v", coils, want)
	}
	fmt.Printf("[5/%d] master wrote coils 0..2 = %v and read them back\n", steps, want)

	// Step 6: the read-only bank, which the slave seeded and the master never
	// wrote — function code 0x04.
	inputCtx, cancelInput := context.WithTimeout(ctx, timeout)
	defer cancelInput()
	inputs, err := cli.ReadInputRegisters(inputCtx, unit, 0, 4)
	if err != nil {
		return fmt.Errorf("read input registers 0..3: %w", err)
	}
	if len(inputs) != 4 || inputs[0] != 100 || inputs[1] != 200 || inputs[2] != 300 || inputs[3] != 400 {
		return fmt.Errorf("input registers 0..3 read back %v, want [100 200 300 400]", inputs)
	}
	fmt.Printf("[6/%d] master read input registers 0..3 = %v\n", steps, inputs)

	fmt.Println("[done] RTU round trips verified; slave stopped")
	return nil
}

// transportNetwork maps the demo's link to the library's transport: the RTU
// framing in both cases, over TCP or over a serial device.
func transportNetwork(transport string) gmodbus.Network {
	if transport == "pty" {
		return gmodbus.NetworkRTU
	}
	return gmodbus.NetworkRTUOverTCP
}

// serialConfig is the line configuration of the serial link. The network
// transports reject it, so the rtu-tcp run leaves it nil.
func serialConfig(transport string, baud int) *gmodbus.SerialConfig {
	if transport != "pty" {
		return nil
	}
	return &gmodbus.SerialConfig{Baud: baud}
}

// link opens the demo's link and returns the slave's address, the master's
// address, one line describing it, and the cleanup that tears it down. For
// rtu-tcp the two addresses are the same socket, because the server is already
// listening when the client dials and the master side is the dial. For pty they
// are the two device paths socat created, which is why socat stays alive until
// cleanup runs.
func link(ctx context.Context, transport, addr string, baud int, socat string) (slave, master, summary string, cleanup func(), err error) {
	if transport == "rtu-tcp" {
		return addr, "", "link: RTU frames over loopback TCP, no serial device involved", func() {}, nil
	}

	slavePath, masterPath, stop, err := ptyPair(ctx, socat)
	if err != nil {
		return "", "", "", nil, err
	}
	return slavePath, masterPath,
		fmt.Sprintf("link: pseudo-terminal pair %s <-> %s via socat, %d 8N1 on the serial end", masterPath, slavePath, baud), stop, nil
}

// ptyPair starts "socat -d -d pty,raw,echo=0 pty,raw,echo=0" and reads the two
// device paths it reports on stderr. socat holds the master side of both pseudo
// terminals and forwards between them, so the two paths behave like the two ends
// of one serial cable: raw (no line discipline rework) and without echo, because
// an echoed byte would look like a second frame to the peer.
func ptyPair(ctx context.Context, socat string) (slave, master string, stop func(), err error) {
	bin, err := exec.LookPath(socat)
	if err != nil {
		return "", "", nil, fmt.Errorf("%s not found: install socat (brew install socat) or use -transport rtu-tcp", socat)
	}
	cmd := exec.CommandContext(ctx, bin, "-d", "-d", "pty,raw,echo=0", "pty,raw,echo=0")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", "", nil, fmt.Errorf("capture socat's stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", "", nil, fmt.Errorf("start socat: %w", err)
	}
	stop = func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	var paths []string
	scanner := bufio.NewScanner(stderr)
	for len(paths) < 2 && scanner.Scan() {
		line := scanner.Text()
		if _, after, ok := strings.Cut(line, "PTY is "); ok {
			paths = append(paths, strings.TrimSpace(after))
		}
	}
	if err := scanner.Err(); err != nil {
		stop()
		return "", "", nil, fmt.Errorf("read socat's pty paths: %w", err)
	}
	if len(paths) < 2 {
		stop()
		return "", "", nil, errors.New("socat reported fewer than two pseudo terminals")
	}
	// The first pseudo terminal is the cable's master end, the second its slave
	// end; naming them after the Modbus role keeps the steps readable.
	return paths[1], paths[0], stop, nil
}
