// Command read-holding reads holding registers from one Modbus TCP device and
// prints the values.
//
// The device address is required: there is no sensible default host. Point it
// at a real Modbus TCP server (a simulator works) and the example prints the
// registers; without a device it exits non-zero with the transport error
// instead of panicking.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/maxid/gmodbus"
)

func main() {
	addr := flag.String("addr", "", "Modbus TCP device address, host:port (required)")
	unit := flag.Int("unit", 1, "Modbus unit id, 0..255")
	start := flag.Int("start", 0, "first holding-register address, 0..65535")
	quantity := flag.Int("quantity", 4, "number of holding registers to read, 1..125")
	timeout := flag.Duration("timeout", 3*time.Second, "deadline for the connect and the read")
	flag.Parse()

	switch {
	case *addr == "":
		fmt.Fprintln(os.Stderr, "missing -addr: pass the Modbus TCP device address, for example -addr 192.0.2.10:502")
		flag.Usage()
		os.Exit(2)
	case *unit < 0 || *unit > 255:
		log.Fatalf("invalid -unit %d: must be 0..255", *unit)
	case *start < 0 || *start > 65535:
		log.Fatalf("invalid -start %d: must be 0..65535", *start)
	case *quantity < 1 || *quantity > 125:
		log.Fatalf("invalid -quantity %d: must be 1..125", *quantity)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cli, err := gmodbus.Dial(ctx, gmodbus.Config{Addr: *addr, Timeout: *timeout})
	if err != nil {
		log.Fatalf("modbus: connect to %s: %v", *addr, err)
	}
	defer cli.Close()

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
