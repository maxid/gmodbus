// Command server runs a Modbus TCP server with an in-memory register map and
// logs every write. It is a minimal device simulator: the built-in MemoryStore
// answers the standard function codes, and OnWrite shows how to react to a
// change.
//
// Run it, then point the read-holding example at the same address:
//
//	go run ./examples/server -addr 127.0.0.1:5020 &
//	go run ./examples/read-holding -addr 127.0.0.1:5020 -start 0 -quantity 4
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/maxid/gmodbus"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5020", "listen address, host:port")
	unit := flag.Int("unit", 1, "unit id to seed, 1..255")
	flag.Parse()
	if *unit < 1 || *unit > 255 {
		log.Fatalf("invalid -unit %d: must be 1..255", *unit)
	}

	store := gmodbus.NewMemoryStore(nil)
	seed := store.AddUnit(uint8(*unit), gmodbus.UnitConfig{})
	if err := seed.SetHoldingRegisters(0, []uint16{10, 20, 30, 40}); err != nil {
		log.Fatalf("seed holding registers: %v", err)
	}
	if err := seed.SetInputRegisters(0, []uint16{100, 200, 300, 400}); err != nil {
		log.Fatalf("seed input registers: %v", err)
	}
	store.OnWrite(func(event gmodbus.WriteEvent) {
		if len(event.Registers) > 0 {
			log.Printf("write unit %d registers at %d: %v", event.Unit, event.Address, event.Registers)
			return
		}
		log.Printf("write unit %d coils at %d: %v", event.Unit, event.Address, event.Coils)
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
		Network: gmodbus.NetworkTCP,
		Addr:    *addr,
		Store:   store,
		Device: &gmodbus.DeviceIdentification{
			VendorName:         "gmodbus examples",
			ProductCode:        "SERVER-1",
			MajorMinorRevision: "1.0",
		},
		OnError: func(err error) { log.Printf("server error: %v", err) },
	})
	if err != nil {
		log.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Close() }()

	fmt.Printf("Modbus TCP server on %s, unit %d; press Ctrl-C to stop\n", srv.Addr(), *unit)
	<-ctx.Done()
	_ = srv.Close()
}
