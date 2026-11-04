package gmodbus

// Benchmarks for the two costs a deployment cares about: a whole read over a
// real connection, and the framing arithmetic under it. Neither is a capacity
// claim — see docs/PERFORMANCE.md — and both run the package's own server over a
// real loopback socket or a real RTU frame, never a mock.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/maxid/gnety/codec/modbus"
)

// BenchmarkReadHolding measures one ReadHoldingRegisters call end to end: the
// request is encoded, written, decoded by the in-process server, answered from
// the register map, and decoded again by the client. The connection is a real
// loopback TCP socket and is established outside the timed region.
func BenchmarkReadHolding(b *testing.B) {
	for _, quantity := range []uint16{4, 125} {
		b.Run(fmt.Sprintf("registers=%d", quantity), func(b *testing.B) {
			client, unit := benchClient(b, NetworkTCP)
			ctx := context.Background()
			// Seed the bank so the answer carries values rather than zeros.
			values := make([]uint16, quantity)
			for i := range values {
				values[i] = uint16(i)
			}
			if err := unit.SetHoldingRegisters(0, values); err != nil {
				b.Fatalf("seed: %v", err)
			}
			// One exchange outside the timed region, so the first connection's
			// read buffers are not part of the first sample.
			if _, err := client.ReadHoldingRegisters(ctx, 1, 0, quantity); err != nil {
				b.Fatalf("warm-up read: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := client.ReadHoldingRegisters(ctx, 1, 0, quantity); err != nil {
					b.Fatalf("ReadHoldingRegisters: %v", err)
				}
			}
		})
	}
}

// BenchmarkWriteSingleRegister measures one write end to end over the same
// loopback connection. A write costs one round trip too, so the difference from a
// read is the PDU size, not the number of turns on the wire.
func BenchmarkWriteSingleRegister(b *testing.B) {
	client, _ := benchClient(b, NetworkTCP)
	ctx := context.Background()
	if err := client.WriteSingleRegister(ctx, 1, 0, 1); err != nil {
		b.Fatalf("warm-up write: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := client.WriteSingleRegister(ctx, 1, 0, uint16(i)); err != nil {
			b.Fatalf("WriteSingleRegister: %v", err)
		}
	}
}

// BenchmarkRTUFrame measures the framing alone, with no socket, no goroutine and
// no store: encoding one CRC-framed RTU request, and decoding one response into a
// typed message. On a serial line these costs are invisible next to the wire
// time, which is exactly why they are measured separately from the transport.
func BenchmarkRTUFrame(b *testing.B) {
	request := &modbus.ReadHoldingRegistersRequest{Address: 0, Quantity: 4}
	response := modbus.RTUFrame{
		UnitID: 1,
		PDU:    []byte{byte(modbus.FuncReadHoldingRegisters), 8, 0, 1, 0, 2, 0, 3, 0, 4},
	}
	rtu := modbus.RTUOptions{}

	b.Run("encode", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			var pdu bytes.Buffer
			if err := modbus.EncodePDU(&pdu, request); err != nil {
				b.Fatalf("EncodePDU: %v", err)
			}
			if _, err := rtu.Marshal(modbus.RTUFrame{UnitID: 1, PDU: pdu.Bytes()}); err != nil {
				b.Fatalf("Marshal: %v", err)
			}
		}
	})

	raw, err := rtu.Marshal(response)
	if err != nil {
		b.Fatalf("seed the response frame: %v", err)
	}
	// One buffered reader per connection is what a deployment has, so the
	// benchmark keeps one and resets it per frame instead of allocating 4 KiB of
	// buffer inside the measured loop.
	reader := bufio.NewReader(bytes.NewReader(raw))
	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			reader.Reset(bytes.NewReader(raw))
			if _, err := rtu.ReadResponse(reader, modbus.MaxPDU, nil, request); err != nil {
				b.Fatalf("ReadResponse: %v", err)
			}
		}
	})
}

// benchClient starts an in-process server on the requested transport and a
// client connected to it, and returns the client with the seeded unit.
func benchClient(b *testing.B, network Network) (*Client, *UnitStore) {
	b.Helper()
	store := NewMemoryStore(nil)
	unit := store.AddUnit(1, UnitConfig{HoldingRegisters: 128, Coils: 128, InputRegisters: 128, DiscreteInputs: 128})
	srv, err := NewServer(context.Background(), ServerConfig{
		Network: network,
		Addr:    "127.0.0.1:0",
		Store:   store,
	})
	if err != nil {
		b.Fatalf("NewServer: %v", err)
	}
	b.Cleanup(func() { _ = srv.Close() })
	client, err := NewClient(context.Background(), ClientConfig{
		Network: network,
		Addr:    srv.Addr(),
		Timeout: DefaultTimeout,
	})
	if err != nil {
		b.Fatalf("NewClient: %v", err)
	}
	b.Cleanup(func() { _ = client.Close() })
	return client, unit
}
