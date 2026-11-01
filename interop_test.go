package gmodbus

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	sim "github.com/simonvetter/modbus"
)

// TestInteropOurClientToTheirServer checks our client against an independent
// Modbus TCP server implementation.
func TestInteropOurClientToTheirServer(t *testing.T) {
	port := freeTCPPort(t)
	handler := &interopHandler{
		coils:   make([]bool, 64),
		inputs:  make([]bool, 64),
		holding: make([]uint16, 64),
		input:   make([]uint16, 64),
	}
	handler.inputs[0] = true
	handler.inputs[2] = true
	for i := range handler.input {
		handler.input[i] = uint16(i + 100)
	}
	srv, err := sim.NewServer(&sim.ServerConfiguration{
		URL:     fmt.Sprintf("tcp://127.0.0.1:%d", port),
		Timeout: 5 * time.Second,
	}, handler)
	if err != nil {
		t.Fatalf("their server: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("their server start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	cli, err := NewClient(context.Background(), ClientConfig{
		Network: NetworkTCP,
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("our client: %v", err)
	}
	defer func() { _ = cli.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := cli.WriteSingleCoil(ctx, 1, 3, true); err != nil {
		t.Fatalf("WriteSingleCoil: %v", err)
	}
	coils, err := cli.ReadCoils(ctx, 1, 3, 1)
	if err != nil || !coils[0] {
		t.Fatalf("ReadCoils = %v, %v", coils, err)
	}
	if err := cli.WriteSingleRegister(ctx, 1, 2, 0x1234); err != nil {
		t.Fatalf("WriteSingleRegister: %v", err)
	}
	registers, err := cli.ReadHoldingRegisters(ctx, 1, 2, 1)
	if err != nil || registers[0] != 0x1234 {
		t.Fatalf("ReadHoldingRegisters = %v, %v", registers, err)
	}
	if err := cli.WriteMultipleCoils(ctx, 1, 4, []bool{true, false, true}); err != nil {
		t.Fatalf("WriteMultipleCoils: %v", err)
	}
	if err := cli.WriteMultipleRegisters(ctx, 1, 8, []uint16{7, 8}); err != nil {
		t.Fatalf("WriteMultipleRegisters: %v", err)
	}
	registers, err = cli.ReadHoldingRegisters(ctx, 1, 8, 2)
	if err != nil || registers[0] != 7 || registers[1] != 8 {
		t.Fatalf("holding registers = %v, %v", registers, err)
	}
	inputs, err := cli.ReadDiscreteInputs(ctx, 1, 0, 3)
	if err != nil || !inputs[0] || inputs[1] || !inputs[2] {
		t.Fatalf("discrete inputs = %v, %v", inputs, err)
	}
	inputRegisters, err := cli.ReadInputRegisters(ctx, 1, 0, 3)
	if err != nil || inputRegisters[2] != 102 {
		t.Fatalf("input registers = %v, %v", inputRegisters, err)
	}
}

// TestInteropTheirClientToOurServer checks an independent Modbus client against
// our server over every transport it speaks.
func TestInteropTheirClientToOurServer(t *testing.T) {
	cases := []struct {
		name    string
		network Network
		scheme  string
	}{
		{"tcp", NetworkTCP, "tcp"},
		{"udp", NetworkUDP, "udp"},
		{"rtu-tcp", NetworkRTUOverTCP, "rtuovertcp"},
		{"rtu-udp", NetworkRTUOverUDP, "rtuoverudp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, err := NewServer(context.Background(), ServerConfig{
				Network: tc.network,
				Addr:    "127.0.0.1:0",
			})
			if err != nil {
				t.Fatalf("our server: %v", err)
			}
			defer func() { _ = srv.Close() }()
			unit := srv.store.unitFor(1)
			if err := unit.SetDiscreteInputs(0, []bool{true, false, true}); err != nil {
				t.Fatal(err)
			}
			if err := unit.SetInputRegisters(0, []uint16{100, 101, 102}); err != nil {
				t.Fatal(err)
			}

			cli, err := sim.NewClient(&sim.ClientConfiguration{
				URL:     fmt.Sprintf("%s://%s", tc.scheme, srv.Addr()),
				Timeout: 2 * time.Second,
				Speed:   19200,
			})
			if err != nil {
				t.Fatalf("their client: %v", err)
			}
			defer func() { _ = cli.Close() }()
			if err := cli.SetUnitId(1); err != nil {
				t.Fatalf("SetUnitId: %v", err)
			}
			if err := cli.Open(); err != nil {
				t.Fatalf("their client open: %v", err)
			}

			coils, err := cli.ReadCoils(0, 3)
			if err != nil || len(coils) != 3 {
				t.Fatalf("ReadCoils = %v, %v", coils, err)
			}
			if err := cli.WriteCoil(3, true); err != nil {
				t.Fatalf("WriteCoil: %v", err)
			}
			if value, err := cli.ReadCoil(3); err != nil || !value {
				t.Fatalf("ReadCoil = %v, %v", value, err)
			}
			if err := cli.WriteRegister(2, 0x1234); err != nil {
				t.Fatalf("WriteRegister: %v", err)
			}
			if value, err := cli.ReadRegister(2, sim.HOLDING_REGISTER); err != nil || value != 0x1234 {
				t.Fatalf("ReadRegister = %#04x, %v", value, err)
			}
			if err := cli.WriteRegisters(4, []uint16{7, 8}); err != nil {
				t.Fatalf("WriteRegisters: %v", err)
			}
			if values, err := cli.ReadRegisters(4, 2, sim.HOLDING_REGISTER); err != nil || values[0] != 7 || values[1] != 8 {
				t.Fatalf("ReadRegisters = %v, %v", values, err)
			}
			if values, err := cli.ReadDiscreteInputs(0, 3); err != nil || !values[0] || values[1] || !values[2] {
				t.Fatalf("ReadDiscreteInputs = %v, %v", values, err)
			}
			if values, err := cli.ReadRegisters(0, 3, sim.INPUT_REGISTER); err != nil || values[2] != 102 {
				t.Fatalf("ReadRegisters(input) = %v, %v", values, err)
			}
		})
	}
}

// interopHandler backs the independent TCP server in the first interop test.
type interopHandler struct {
	coils   []bool
	inputs  []bool
	holding []uint16
	input   []uint16
}

func (h *interopHandler) HandleCoils(req *sim.CoilsRequest) ([]bool, error) {
	if req.IsWrite {
		copy(h.coils[req.Addr:], req.Args)
		return nil, nil
	}
	return append([]bool(nil), h.coils[req.Addr:req.Addr+req.Quantity]...), nil
}

func (h *interopHandler) HandleDiscreteInputs(req *sim.DiscreteInputsRequest) ([]bool, error) {
	return append([]bool(nil), h.inputs[req.Addr:req.Addr+req.Quantity]...), nil
}

func (h *interopHandler) HandleHoldingRegisters(req *sim.HoldingRegistersRequest) ([]uint16, error) {
	if req.IsWrite {
		copy(h.holding[req.Addr:], req.Args)
		return nil, nil
	}
	return append([]uint16(nil), h.holding[req.Addr:req.Addr+req.Quantity]...), nil
}

func (h *interopHandler) HandleInputRegisters(req *sim.InputRegistersRequest) ([]uint16, error) {
	return append([]uint16(nil), h.input[req.Addr:req.Addr+req.Quantity]...), nil
}

// freeTCPPort reserves and releases a local port for a server that insists on a
// fixed address.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
