package gmodbus

import (
	"context"
	"errors"
	"testing"

	"github.com/maxid/gnety/codec/modbus"
)

func TestUnitConfigDefaults(t *testing.T) {
	store := NewMemoryStore(nil)
	unit, ok := store.Unit(1)
	if !ok {
		t.Fatal("default store has no unit 1")
	}
	if got, err := unit.Coils(0, DefaultUnitSize); err != nil || len(got) != DefaultUnitSize {
		t.Fatalf("default coils = %d, err %v", len(got), err)
	}
	small := store.AddUnit(2, UnitConfig{Coils: 8})
	if _, err := small.Coils(0, 8); err != nil {
		t.Fatalf("configured coils: %v", err)
	}
	if _, err := small.Coils(0, 9); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("coils past the configured size = %v, want out of range", err)
	}
	if got, err := small.InputRegisters(0, DefaultUnitSize); err != nil || len(got) != DefaultUnitSize {
		t.Fatalf("zero fields did not take the default: %d, err %v", len(got), err)
	}
}

func TestUnitStoreBanks(t *testing.T) {
	store := NewMemoryStore(nil)
	unit, _ := store.Unit(1)

	if err := unit.SetCoils(0, []bool{true, false, true}); err != nil {
		t.Fatalf("SetCoils: %v", err)
	}
	if got, err := unit.Coils(0, 3); err != nil || !equalBool(got, []bool{true, false, true}) {
		t.Fatalf("Coils = %v, err %v", got, err)
	}
	if err := unit.SetDiscreteInputs(0, []bool{false, true}); err != nil {
		t.Fatalf("SetDiscreteInputs: %v", err)
	}
	if got, err := unit.DiscreteInputs(0, 2); err != nil || !equalBool(got, []bool{false, true}) {
		t.Fatalf("DiscreteInputs = %v, err %v", got, err)
	}
	if err := unit.SetHoldingRegisters(0, []uint16{1, 2}); err != nil {
		t.Fatalf("SetHoldingRegisters: %v", err)
	}
	if got, err := unit.HoldingRegisters(0, 2); err != nil || !equalUint16(got, []uint16{1, 2}) {
		t.Fatalf("HoldingRegisters = %v, err %v", got, err)
	}
	if err := unit.SetInputRegisters(0, []uint16{3, 4}); err != nil {
		t.Fatalf("SetInputRegisters: %v", err)
	}
	if got, err := unit.InputRegisters(0, 2); err != nil || !equalUint16(got, []uint16{3, 4}) {
		t.Fatalf("InputRegisters = %v, err %v", got, err)
	}

	if _, err := unit.Coils(DefaultUnitSize, 1); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("read past the end = %v, want out of range", err)
	}
	if err := unit.SetHoldingRegisters(DefaultUnitSize-1, []uint16{1, 2}); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("write past the end = %v, want out of range", err)
	}
	if err := unit.SetCoils(0, nil); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("empty write = %v, want out of range", err)
	}
}

func TestUnitStoreMaskWriteAndReadWrite(t *testing.T) {
	store := NewMemoryStore(nil)
	unit, _ := store.Unit(1)
	if err := unit.SetHoldingRegisters(0, []uint16{0x0012, 0x0034}); err != nil {
		t.Fatal(err)
	}
	echo, value, err := unit.MaskWrite(0, 0x00f2, 0x0025)
	if err != nil {
		t.Fatalf("MaskWrite: %v", err)
	}
	if want := (uint16(0x0012) & 0x00f2) | (0x0025 &^ 0x00f2); value != want {
		t.Fatalf("value = %#04x, want %#04x", value, want)
	}
	if echo.Address != 0 || echo.AndMask != 0x00f2 || echo.OrMask != 0x0025 {
		t.Fatalf("echo = %#v", echo)
	}
	values, err := unit.ReadWrite(0, 2, 1, []uint16{0x00aa})
	if err != nil {
		t.Fatalf("ReadWrite: %v", err)
	}
	if want := []uint16{value, 0x00aa}; !equalUint16(values, want) {
		t.Fatalf("ReadWrite = %v, want %v", values, want)
	}
	if _, err := unit.ReadWrite(0, 1, DefaultUnitSize, []uint16{1}); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("out-of-range ReadWrite = %v", err)
	}
}

func TestMemoryStoreOnWrite(t *testing.T) {
	store := NewMemoryStore(nil)
	var events []WriteEvent
	store.OnWrite(func(event WriteEvent) { events = append(events, event) })
	unit, _ := store.Unit(1)
	ctx := context.Background()

	handle := func(req modbus.Message) {
		t.Helper()
		if _, err := store.Handle(ctx, 1, req); err != nil {
			t.Fatalf("Handle(%T): %v", req, err)
		}
	}
	handle(&modbus.WriteSingleCoil{Address: 1, Value: true})
	handle(&modbus.WriteMultipleRegistersRequest{Address: 2, Values: []uint16{5, 6}})
	handle(&modbus.MaskWriteRegister{Address: 0, AndMask: 0xff, OrMask: 0x0f})
	handle(&modbus.ReadWriteMultipleRegistersRequest{ReadAddress: 0, ReadQuantity: 1, WriteAddress: 4, WriteValues: []uint16{7}})
	handle(&modbus.ReadCoilsRequest{Address: 0, Quantity: 1})

	if len(events) != 4 {
		t.Fatalf("writes = %d, want 4", len(events))
	}
	if events[0].Kind != Coils || events[0].Address != 1 || !events[0].Coils[0] {
		t.Fatalf("coil event = %#v", events[0])
	}
	if events[1].Kind != HoldingRegisters || !equalUint16(events[1].Registers, []uint16{5, 6}) {
		t.Fatalf("register event = %#v", events[1])
	}
	if events[2].Kind != HoldingRegisters || len(events[2].Registers) != 1 {
		t.Fatalf("mask event = %#v", events[2])
	}
	if events[3].Kind != HoldingRegisters || !equalUint16(events[3].Registers, []uint16{7}) {
		t.Fatalf("read/write event = %#v", events[3])
	}
	if value, _ := unit.HoldingRegister(2); value != 5 {
		t.Fatalf("register 2 = %d", value)
	}
}

func TestMemoryStoreBroadcast(t *testing.T) {
	store := NewMemoryStore(nil)
	two := store.AddUnit(2, UnitConfig{})
	if _, err := store.Handle(context.Background(), 0, &modbus.WriteMultipleRegistersRequest{Address: 0, Values: []uint16{42}}); err != nil {
		t.Fatalf("broadcast write: %v", err)
	}
	for _, unit := range []*UnitStore{store.unitFor(1), two} {
		if value, _ := unit.HoldingRegister(0); value != 42 {
			t.Fatalf("unit %d = %d, want 42", unit.Unit(), value)
		}
	}
	resp, err := store.Handle(context.Background(), 0, &modbus.ReadHoldingRegistersRequest{Address: 0, Quantity: 1})
	if err != nil || resp != nil {
		t.Fatalf("broadcast read = %#v, %v; want no response", resp, err)
	}
}

func TestMemoryStoreLazilyCreatesUnits(t *testing.T) {
	store := NewMemoryStore(nil)
	if _, err := store.Handle(context.Background(), 9, &modbus.WriteSingleRegister{Address: 0, Value: 3}); err != nil {
		t.Fatalf("write to a new unit: %v", err)
	}
	unit, ok := store.Unit(9)
	if !ok {
		t.Fatal("unit 9 was not created")
	}
	if value, _ := unit.HoldingRegister(0); value != 3 {
		t.Fatalf("unit 9 register 0 = %d", value)
	}
}

func TestDeviceIdentificationSplit(t *testing.T) {
	device := &DeviceIdentification{
		VendorName:         "ACME",
		ProductCode:        "P1",
		MajorMinorRevision: "1.0",
	}
	// The fixed header is 7 bytes; ACME is 2+4, P1 is 2+2, 1.0 is 2+3.
	// A 15-byte PDU fits the vendor name and stops before the product code.
	resp, err := device.handle(&modbus.ReadDeviceIdentificationRequest{ReadCode: modbus.DeviceIDBasic}, 15)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	identification := resp.(*modbus.ReadDeviceIdentificationResponse)
	if len(identification.Objects) != 1 || string(identification.Objects[0].Value) != "ACME" {
		t.Fatalf("objects = %#v", identification.Objects)
	}
	if !identification.MoreFollows || identification.NextObjectID != modbus.DeviceIDProductCode {
		t.Fatalf("split = more %v next %#x", identification.MoreFollows, identification.NextObjectID)
	}
	if _, err := identification.PDU(); err != nil {
		t.Fatalf("encoded split response: %v", err)
	}

	next, err := device.handle(&modbus.ReadDeviceIdentificationRequest{ReadCode: modbus.DeviceIDIndividual, ObjectID: identification.NextObjectID}, modbus.MaxPDU)
	if err != nil {
		t.Fatalf("continuation: %v", err)
	}
	nextResp := next.(*modbus.ReadDeviceIdentificationResponse)
	if len(nextResp.Objects) != 1 || string(nextResp.Objects[0].Value) != "P1" {
		t.Fatalf("continuation objects = %#v", nextResp.Objects)
	}

	if _, err := device.handle(&modbus.ReadDeviceIdentificationRequest{ReadCode: modbus.DeviceIDIndividual, ObjectID: 0x40}, modbus.MaxPDU); err == nil {
		t.Fatal("unknown object did not fail")
	}
}
