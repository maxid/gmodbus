// Command read-holding reads holding registers from one Modbus device and
// prints the values.
//
// The device address is required: there is no sensible default host. Point it
// at a real Modbus server (the server example or a simulator works) and the
// example prints the registers; without a device it exits non-zero with the
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
	"slices"
	"strings"
	"time"

	"github.com/maxid/gmodbus"
	"github.com/pion/dtls/v2"
)

// networks lists the transports the example can dial.
var networks = []string{"tcp", "tls", "udp", "dtls", "rtu", "rtu-tcp", "rtu-udp"}

func main() {
	network := flag.String("network", "tcp", "transport: "+strings.Join(networks, "|"))
	addr := flag.String("addr", "", `device address: host:port, or the serial path for -network rtu (required)`)
	unit := flag.Int("unit", 1, "Modbus unit id, 0..255")
	start := flag.Int("start", 0, "first holding-register address, 0..65535")
	quantity := flag.Int("quantity", 4, "number of holding registers to read, 1..125")
	baud := flag.Int("baud", 19200, "serial baud rate for -network rtu")
	cacert := flag.String("cacert", "", "PEM CA bundle for -network tls or dtls; empty uses the system roots for TLS")
	timeout := flag.Duration("timeout", 3*time.Second, "deadline for the connect and the read")
	flag.Parse()

	if !slices.Contains(networks, *network) {
		log.Fatalf("invalid -network %q: must be one of %v", *network, networks)
	}
	switch {
	case *addr == "":
		fmt.Fprintln(os.Stderr, "missing -addr: pass the device address, for example -addr 192.0.2.10:502")
		flag.Usage()
		os.Exit(2)
	case *unit < 0 || *unit > 255:
		log.Fatalf("invalid -unit %d: must be 0..255", *unit)
	case *start < 0 || *start > 65535:
		log.Fatalf("invalid -start %d: must be 0..65535", *start)
	case *quantity < 1 || *quantity > 125:
		log.Fatalf("invalid -quantity %d: must be 1..125", *quantity)
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
			log.Fatalf("modbus: %v", err)
		}
		cfg.TLS = &tls.Config{RootCAs: pool}
	}
	if *network == string(gmodbus.NetworkDTLS) {
		pool, err := caPool(*cacert)
		if err != nil {
			log.Fatalf("modbus: %v", err)
		}
		if pool == nil {
			log.Fatal("modbus: -cacert is required for -network dtls")
		}
		cfg.DTLS = &dtls.Config{RootCAs: pool}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cli, err := gmodbus.NewClient(ctx, cfg)
	if err != nil {
		log.Fatalf("modbus: connect to %s: %v", *addr, err)
	}
	defer func() { _ = cli.Close() }()

	registers, err := cli.ReadHoldingRegisters(ctx, uint8(*unit), uint16(*start), uint16(*quantity))
	if err != nil {
		var exception *gmodbus.Error
		if errors.As(err, &exception) {
			log.Fatalf("modbus: %s rejected the read (unit %d, function %d, exception code %d)",
				*addr, exception.Unit, exception.Function, exception.Code)
		}
		log.Fatalf("modbus: no usable response from %s: %v", *addr, err)
	}

	fmt.Printf("holding registers from %s (unit %d):\n", *addr, *unit)
	for i, value := range registers {
		fmt.Printf("  [0x%04X] = %d (0x%04X)\n", *start+i, value, value)
	}
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
