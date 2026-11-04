//go:build integration

package interop

// The serial direction of the interoperability matrix: RTU over a real serial
// device path, without a serial device.
//
// Two pseudo-terminal pairs stand in for two RS-485 nodes and the cable between
// them: the test bridges the two master sides (a null-modem cable) and both
// gmodbus ends open the slave side by path with the serial transport, so the
// device open, the line parameters and the CRC-framed RTU framing are the real
// code. No hardware and no Docker — only a /dev/ptmx, which Linux and macOS both
// provide; the file is integration-tagged because it touches real device nodes.

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/maxid/gmodbus"
)

// TestRTUOverPseudoTerminal runs one RTU master and one RTU slave over a bridged
// pseudo-terminal pair and verifies the round trips on both directions of the
// link.
func TestRTUOverPseudoTerminal(t *testing.T) {
	masterPTY, masterTTY, err := pty.Open()
	if err != nil {
		t.Skipf("this machine cannot open a pseudo terminal: %v", err)
	}
	slavePTY, slaveTTY, err := pty.Open()
	if err != nil {
		_ = masterPTY.Close()
		_ = masterTTY.Close()
		t.Skipf("this machine cannot open a second pseudo terminal: %v", err)
	}

	// The cable: everything written to one master appears on the other, like a
	// null-modem adapter between two devices.
	var copyWG sync.WaitGroup
	for _, pair := range [][2]io.ReadWriter{{masterPTY, slavePTY}, {slavePTY, masterPTY}} {
		copyWG.Add(1)
		go func(from, to io.ReadWriter) {
			defer copyWG.Done()
			_, _ = io.Copy(to, from)
		}(pair[0], pair[1])
	}
	// Closing the four descriptors ends both copies; the wait has to come after
	// the closes, which is why this is one cleanup rather than three.
	t.Cleanup(func() {
		_ = masterPTY.Close()
		_ = masterTTY.Close()
		_ = slavePTY.Close()
		_ = slaveTTY.Close()
		copyWG.Wait()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The slave opens its own end of the cable by path, exactly as it would open
	// /dev/ttyUSB0, and the line parameters are applied by the serial driver.
	store := seededStore(t)
	srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
		Network: gmodbus.NetworkRTU,
		Addr:    slaveTTY.Name(),
		Serial:  &gmodbus.SerialConfig{Baud: 19200},
		Store:   store,
	})
	if err != nil {
		t.Fatalf("our RTU server on %s: %v", slaveTTY.Name(), err)
	}
	defer func() { _ = srv.Close() }()
	t.Logf("RTU slave on %s, master on %s", slaveTTY.Name(), masterTTY.Name())

	cli, err := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
		Network: gmodbus.NetworkRTU,
		Addr:    masterTTY.Name(),
		Serial:  &gmodbus.SerialConfig{Baud: 19200},
		Timeout: 2 * time.Second,
		Retries: 3,
	})
	if err != nil {
		t.Fatalf("our RTU client on %s: %v", masterTTY.Name(), err)
	}
	defer func() { _ = cli.Close() }()

	holding, err := cli.ReadHoldingRegisters(ctx, 1, 0, 4)
	if err != nil || !slices.Equal(holding, []uint16{10, 20, 30, 40}) {
		t.Fatalf("ReadHoldingRegisters = %v, %v, want [10 20 30 40]", holding, err)
	}
	discrete, err := cli.ReadDiscreteInputs(ctx, 1, 0, 3)
	if err != nil || !slices.Equal(discrete, []bool{true, false, true}) {
		t.Fatalf("ReadDiscreteInputs = %v, %v, want [true false true]", discrete, err)
	}
	if err := cli.WriteSingleRegister(ctx, 1, 2, 0x1234); err != nil {
		t.Fatalf("WriteSingleRegister over RTU: %v", err)
	}
	if got, err := cli.ReadHoldingRegisters(ctx, 1, 2, 1); err != nil || !slices.Equal(got, []uint16{0x1234}) {
		t.Fatalf("holding 2 after the write = %v, %v, want [0x1234]", got, err)
	}
	if err := cli.WriteMultipleCoils(ctx, 1, 4, []bool{true, false, true}); err != nil {
		t.Fatalf("WriteMultipleCoils over RTU: %v", err)
	}
	if got, err := cli.ReadCoils(ctx, 1, 4, 3); err != nil || !slices.Equal(got, []bool{true, false, true}) {
		t.Fatalf("coils 4..6 after the write = %v, %v", got, err)
	}

	// An address the slave does not hold must come back as an exception over RTU
	// too: the CRC-framed exception path is a different encoder from MBAP's.
	_, err = cli.ReadHoldingRegisters(ctx, 1, 2000, 2)
	var rejected *gmodbus.Error
	if !errors.As(err, &rejected) || rejected.Code != 2 {
		t.Fatalf("read of an unheld address = %v, want an illegal data address exception", err)
	}

	// Unit 0 is the RTU broadcast: the write is applied and never answered, so the
	// client returns without waiting for a reply.
	before, ok := store.Unit(1)
	if !ok {
		t.Fatal("the store has no unit 1")
	}
	if err := cli.WriteSingleRegister(ctx, 0, 5, 0x4321); err != nil {
		t.Fatalf("broadcast write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if value, err := before.HoldingRegister(5); err == nil && value == 0x4321 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the broadcast write never reached the slave's holding registers")
}
