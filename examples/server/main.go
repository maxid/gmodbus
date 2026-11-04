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
	addr := flag.String("addr", "127.0.0.1:5020", "listen address, host:port; port 0 picks a free one")
	unit := flag.Int("unit", 1, "unit id to seed, 1..255")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `server runs a Modbus TCP server with a seeded in-memory register map: holding
registers 0..3 hold 10 20 30 40, input registers 0..3 hold 100 200 300 400,
and every write is logged. It is the device the other examples talk to, and it
answers until Ctrl-C.

Point a real Modbus master (modpoll, pymodbus, a PLC's engineering tool) at it
to check a client, or use it as the far end of a read-holding run. A port is
printed once the listener is bound, so -addr 127.0.0.1:0 is the way to avoid a
port already in use.

Examples:
  go run ./examples/server
  go run ./examples/server -addr 127.0.0.1:0 -unit 7
  go run ./examples/read-holding -addr 127.0.0.1:5020 -start 0 -quantity 4   # in another shell

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *unit < 1 || *unit > 255 {
		log.Fatalf("server: -unit %d is not 1..255", *unit)
	}

	store := gmodbus.NewMemoryStore(nil)
	seed := store.AddUnit(uint8(*unit), gmodbus.UnitConfig{})
	if err := seed.SetHoldingRegisters(0, []uint16{10, 20, 30, 40}); err != nil {
		log.Fatalf("server: seed holding registers: %v", err)
	}
	if err := seed.SetInputRegisters(0, []uint16{100, 200, 300, 400}); err != nil {
		log.Fatalf("server: seed input registers: %v", err)
	}
	store.OnWrite(func(event gmodbus.WriteEvent) {
		if len(event.Registers) > 0 {
			log.Printf("write unit %d registers at %d: %v", event.Unit, event.Address, event.Registers)
			return
		}
		log.Printf("write unit %d coils at %d: %v", event.Unit, event.Address, event.Coils)
	})

	// Ctrl-C ends the serve loop instead of killing the process, so the shutdown
	// path (drain, close the listener) still runs and the exit code stays 0.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		log.Fatalf("server: listen on %s: %v", *addr, err)
	}
	defer func() { _ = srv.Close() }()

	fmt.Printf("[serve] Modbus TCP server on %s, unit %d\n", srv.Addr(), *unit)
	fmt.Println("[serve] holding registers 0..3 = [10 20 30 40], input registers 0..3 = [100 200 300 400]")
	fmt.Println("[serve] every write is logged; Ctrl-C stops the server")
	<-ctx.Done()

	// Close drains within ShutdownTimeout and is idempotent, so this also covers
	// the stop the server started on its own when the context ended.
	if err := srv.Close(); err != nil {
		log.Fatalf("server: stop the server: %v", err)
	}
	fmt.Println("[stop] server stopped")
}
