// Command read-holding reads holding registers from one Modbus device and
// prints the values.
//
// The default address is the one the server example listens on, so two shells
// are enough to see a request and a response: `go run ./examples/server` in one,
// this in the other. Point -addr at a real device (or a simulator) and the same
// run prints its registers; a device that does not answer exits non-zero with the
// transport error instead of panicking.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/maxid/gmodbus"
	"github.com/pion/dtls/v2"
)

// networks lists the transports the example can dial.
var networks = []string{"tcp", "tls", "udp", "dtls", "rtu", "rtu-tcp", "rtu-udp"}

func main() {
	network := flag.String("network", "tcp", "transport: "+strings.Join(networks, "|"))
	addr := flag.String("addr", "127.0.0.1:5020", `device address: host:port, or the serial path for -network rtu`)
	unit := flag.Int("unit", 1, "Modbus unit id, 0..255")
	start := flag.Int("start", 0, "first holding-register address, 0..65535")
	quantity := flag.Int("quantity", 4, "number of holding registers to read, 1..125")
	baud := flag.Int("baud", 19200, "serial baud rate for -network rtu")
	cacert := flag.String("cacert", "", "PEM CA bundle for -network tls or dtls; empty uses the system roots for TLS")
	timeout := flag.Duration("timeout", 3*time.Second, "deadline for the connect and the read")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `read-holding reads a block of holding registers (function code 0x03) from one
Modbus device and prints them as decimals and hex, so a device, a gateway or
one of the examples can be checked without writing a program first.

It defaults to the address the server example listens on, so one shell can run
"go run ./examples/server" and another this program with no flags. The read is
one request, so Ctrl-C during a slow device stops it; the exit code is 0 when
interrupted and non-zero when the device does not answer.

Examples:
  go run ./examples/server &                                        # a device to read
  go run ./examples/read-holding
  go run ./examples/read-holding -addr 192.0.2.10:502 -start 0 -quantity 4 -unit 1
  go run ./examples/read-holding -network rtu -addr /dev/ttyUSB0 -baud 9600
  go run ./examples/read-holding -network udp -addr 192.0.2.10:502 -timeout 2s

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if !slices.Contains(networks, *network) {
		log.Fatalf("read-holding: -network %q is not one of %v", *network, networks)
	}
	switch {
	case *addr == "":
		fmt.Fprintln(os.Stderr, "read-holding: -addr is empty: pass the device address, for example -addr 127.0.0.1:5020")
		flag.Usage()
		os.Exit(2)
	case *unit < 0 || *unit > 255:
		log.Fatalf("read-holding: -unit %d is not 0..255", *unit)
	case *start < 0 || *start > 65535:
		log.Fatalf("read-holding: -start %d is not 0..65535", *start)
	case *quantity < 1 || *quantity > 125:
		log.Fatalf("read-holding: -quantity %d is not 1..125", *quantity)
	}

	cfg := gmodbus.ClientConfig{
		Network: gmodbus.Network(*network),
		Addr:    *addr,
		Timeout: *timeout,
	}
	if *network == string(gmodbus.NetworkRTU) {
		cfg.Serial = &gmodbus.SerialConfig{Baud: *baud}
	}
	if *network == string(gmodbus.NetworkTLS) {
		pool, err := caPool(*cacert)
		if err != nil {
			log.Fatalf("read-holding: %v", err)
		}
		cfg.TLS = &tls.Config{RootCAs: pool}
	}
	if *network == string(gmodbus.NetworkDTLS) {
		pool, err := caPool(*cacert)
		if err != nil {
			log.Fatalf("read-holding: %v", err)
		}
		if pool == nil {
			log.Fatalf("read-holding: -cacert is required for -network dtls")
		}
		cfg.DTLS = &dtls.Config{RootCAs: pool}
	}

	// Ctrl-C cancels the attempt instead of killing the process, so the example
	// can say what it was doing and still exit 0.
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(sigCtx, *timeout)
	defer cancel()
	interrupted := func() bool { return errors.Is(sigCtx.Err(), context.Canceled) }

	fmt.Printf("[1/3] connecting to %s over %s\n", *addr, *network)
	cli, err := gmodbus.NewClient(ctx, cfg)
	if err != nil {
		if interrupted() {
			fmt.Println("[stop] interrupted")
			return
		}
		log.Fatalf("read-holding: connect to %s: %v", *addr, err)
	}
	defer func() { _ = cli.Close() }()

	fmt.Printf("[2/3] reading %d holding registers from address %d (unit %d)\n", *quantity, *start, *unit)
	registers, err := cli.ReadHoldingRegisters(ctx, uint8(*unit), uint16(*start), uint16(*quantity))
	if err != nil {
		if interrupted() {
			fmt.Println("[stop] interrupted")
			return
		}
		var exception *gmodbus.Error
		if errors.As(err, &exception) {
			log.Fatalf("read-holding: %s rejected the read (unit %d, function %d, exception code %d)",
				*addr, exception.Unit, exception.Function, exception.Code)
		}
		log.Fatalf("read-holding: no usable response from %s: %v", *addr, err)
	}

	fmt.Printf("[3/3] holding registers from %s (unit %d):\n", *addr, *unit)
	for i, value := range registers {
		fmt.Printf("  [0x%04X] = %d (0x%04X)\n", *start+i, value, value)
	}
	fmt.Println("[done] read holding registers; connection closed")
}

// caPool loads a PEM bundle, or returns nil for the system roots when the path
// is empty.
func caPool(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA bundle %s contains no certificate", path)
	}
	return pool, nil
}
