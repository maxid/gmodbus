package gmodbus

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/maxid/gnety/codec/modbus"
	"github.com/pion/dtls/v2"
)

func TestClientValidatesTransportConfiguration(t *testing.T) {
	cases := map[string]ClientConfig{
		"unknown network":     {Network: "carrier-pigeon", Addr: "127.0.0.1:1"},
		"missing address":     {Network: NetworkUDP},
		"tls on tcp":          {Addr: "127.0.0.1:1", TLS: &tls.Config{}},
		"dtls on tcp":         {Addr: "127.0.0.1:1", DTLS: &dtls.Config{}},
		"serial on tcp":       {Addr: "127.0.0.1:1", Serial: &SerialConfig{}},
		"port on tcp":         {Addr: "127.0.0.1:1", Port: pipePort()},
		"dtls without config": {Network: NetworkDTLS, Addr: "127.0.0.1:1"},
		"rtu without address": {Network: NetworkRTU},
		"rtu with serial and port": {
			Network: NetworkRTU, Port: pipePort(), Serial: &SerialConfig{},
		},
		"rtu with no crc": {
			Network: NetworkRTU, Addr: "/dev/null", RTU: modbus.RTUOptions{NoCRC: true},
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if cli, err := NewClient(context.Background(), cfg); err == nil {
				_ = cli.Close()
				t.Fatal("NewClient succeeded")
			}
		})
	}
}

func TestSerialConfigValidation(t *testing.T) {
	cases := map[string]*SerialConfig{
		"negative baud": {Baud: -1},
		"data bits 9":   {DataBits: 9},
		"stop bits 3":   {StopBits: 3},
		"parity 9":      {Parity: Parity(9)},
	}
	for name, serial := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewClient(context.Background(), ClientConfig{
				Network: NetworkRTU, Addr: "/dev/null", Serial: serial,
			})
			if err == nil {
				t.Fatal("NewClient succeeded")
			}
		})
	}
	// A valid configuration reaches the device open and fails there.
	if cli, err := NewClient(context.Background(), ClientConfig{
		Network: NetworkRTU, Addr: "/nonexistent/gmodbus-serial",
	}); err == nil {
		_ = cli.Close()
		t.Fatal("opening a missing serial device succeeded")
	}
}

// startRTUFake starts a TCP listener that answers each RTU request with the
// frame respond returns.
func startRTUFake(t *testing.T, respond func(modbus.RTUFrame) modbus.RTUFrame) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			frame, err := (modbus.RTUOptions{}).Read(reader, 0, nil)
			if err != nil {
				return
			}
			reply := respond(frame)
			raw, err := reply.Marshal()
			if err != nil {
				return
			}
			if _, err := conn.Write(raw); err != nil {
				return
			}
		}
	}()
	return ln.Addr().String()
}

func TestRTUResponseMismatch(t *testing.T) {
	readResponse := func(unit uint8, function byte) modbus.RTUFrame {
		return modbus.RTUFrame{UnitID: unit, PDU: []byte{function, 0x02, 0x00, 0x01}}
	}
	cases := []struct {
		name     string
		response modbus.RTUFrame
	}{
		{"wrong unit", readResponse(2, modbus.FuncReadHoldingRegisters)},
		{"wrong function", readResponse(1, modbus.FuncReadInputRegisters)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := startRTUFake(t, func(modbus.RTUFrame) modbus.RTUFrame { return tc.response })
			cli, err := NewClient(context.Background(), ClientConfig{
				Network: NetworkRTUOverTCP,
				Addr:    addr,
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			defer cli.Close()
			_, err = cli.ReadHoldingRegisters(context.Background(), 1, 0, 1)
			if !errors.Is(err, ErrResponseMismatch) {
				t.Fatalf("read = %v, want ErrResponseMismatch", err)
			}
			// A mismatch poisons the stream, so the client is closed.
			if _, err := cli.ReadHoldingRegisters(context.Background(), 1, 0, 1); !errors.Is(err, ErrClosed) {
				t.Fatalf("second read = %v, want ErrClosed", err)
			}
		})
	}
}

func TestRTUUnsupportedResponseFunctionIsAProtocolError(t *testing.T) {
	addr := startRTUFake(t, func(modbus.RTUFrame) modbus.RTUFrame {
		// The RTU reader has no framing rule for function 0x7F, so the response
		// cannot be delimited and the stream must not be reused.
		return modbus.RTUFrame{UnitID: 1, PDU: []byte{0x7f}}
	})
	cli, err := NewClient(context.Background(), ClientConfig{
		Network: NetworkRTUOverTCP,
		Addr:    addr,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cli.Close()
	_, err = cli.ReadHoldingRegisters(context.Background(), 1, 0, 1)
	if err == nil {
		t.Fatal("read succeeded")
	}
	if _, err := cli.ReadHoldingRegisters(context.Background(), 1, 0, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("second read = %v, want ErrClosed", err)
	}
}
