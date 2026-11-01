package gmodbus

import (
	"context"
	"sync"

	"github.com/maxid/gnety/codec/modbus"
)

// BankKind names one of the four Modbus data banks.
type BankKind uint8

// Modbus data banks.
const (
	Coils BankKind = iota
	DiscreteInputs
	HoldingRegisters
	InputRegisters
)

// UnitConfig sizes the four banks of one unit. A zero field takes the default
// size (DefaultUnitSize points), so UnitConfig{} is a usable unit.
type UnitConfig struct {
	// Coils is the number of coils, addressed from 0.
	Coils uint16
	// DiscreteInputs is the number of read-only inputs.
	DiscreteInputs uint16
	// HoldingRegisters is the number of read/write registers.
	HoldingRegisters uint16
	// InputRegisters is the number of read-only registers.
	InputRegisters uint16
}

// DefaultUnitSize is how many points a zero UnitConfig field creates.
const DefaultUnitSize = 1024

func (c UnitConfig) normalized() UnitConfig {
	if c.Coils == 0 {
		c.Coils = DefaultUnitSize
	}
	if c.DiscreteInputs == 0 {
		c.DiscreteInputs = DefaultUnitSize
	}
	if c.HoldingRegisters == 0 {
		c.HoldingRegisters = DefaultUnitSize
	}
	if c.InputRegisters == 0 {
		c.InputRegisters = DefaultUnitSize
	}
	return c
}

// WriteEvent describes one successful write in the MemoryStore. The slices are
// copies, so a callback may keep them.
type WriteEvent struct {
	// Unit is the addressed unit. A broadcast write reports one event per unit
	// it was applied to.
	Unit uint8
	// Kind is the bank that changed.
	Kind BankKind
	// Address is the first changed point.
	Address uint16
	// Coils holds the written coil states for Coils and DiscreteInputs.
	Coils []bool
	// Registers holds the written values for HoldingRegisters and
	// InputRegisters.
	Registers []uint16
}

// MemoryStore is the built-in server data model: one independent set of coils,
// discrete inputs, holding registers and input registers per unit id.
//
// It implements the standard function codes the codec models, keeps one lock
// per unit, and reports writes through OnWrite. Units are created on first use
// with the store's default UnitConfig, so any unit id works without setup; a
// broadcast (unit 0 on the RTU transports) writes to every unit that exists at
// that moment and produces no response.
type MemoryStore struct {
	mu          sync.RWMutex
	units       map[uint8]*UnitStore
	defaultUnit UnitConfig
	onWrite     func(WriteEvent)
}

// NewMemoryStore returns a store with an optional initial set of units. When
// units is nil or empty the store starts with unit 1 and the default size, so a
// server works without configuration.
func NewMemoryStore(units map[uint8]UnitConfig) *MemoryStore {
	s := &MemoryStore{units: make(map[uint8]*UnitStore), defaultUnit: UnitConfig{}.normalized()}
	if len(units) == 0 {
		s.AddUnit(1, UnitConfig{})
		return s
	}
	for unit, cfg := range units {
		s.AddUnit(unit, cfg)
	}
	return s
}

// AddUnit creates or replaces the banks of one unit and returns it.
func (s *MemoryStore) AddUnit(unit uint8, cfg UnitConfig) *UnitStore {
	cfg = cfg.normalized()
	u := &UnitStore{
		unit:             unit,
		coils:            make([]bool, cfg.Coils),
		discreteInputs:   make([]bool, cfg.DiscreteInputs),
		holdingRegisters: make([]uint16, cfg.HoldingRegisters),
		inputRegisters:   make([]uint16, cfg.InputRegisters),
	}
	s.mu.Lock()
	s.units[unit] = u
	s.mu.Unlock()
	return u
}

// SetDefaultUnitConfig sets the sizes used when a request addresses a unit that
// does not exist yet. It does not resize existing units.
func (s *MemoryStore) SetDefaultUnitConfig(cfg UnitConfig) {
	s.mu.Lock()
	s.defaultUnit = cfg.normalized()
	s.mu.Unlock()
}

// Unit returns an existing unit. It does not create one.
func (s *MemoryStore) Unit(unit uint8) (*UnitStore, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.units[unit]
	return u, ok
}

// OnWrite installs a callback for successful writes. It runs outside the store
// lock, so it may call back into the store.
func (s *MemoryStore) OnWrite(fn func(WriteEvent)) {
	s.mu.Lock()
	s.onWrite = fn
	s.mu.Unlock()
}

// unitFor returns the unit, creating it with the default configuration when it
// does not exist yet.
func (s *MemoryStore) unitFor(unit uint8) *UnitStore {
	s.mu.RLock()
	u, ok := s.units[unit]
	cfg := s.defaultUnit
	s.mu.RUnlock()
	if ok {
		return u
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.units[unit]; ok {
		return u
	}
	created := &UnitStore{
		unit:             unit,
		coils:            make([]bool, cfg.Coils),
		discreteInputs:   make([]bool, cfg.DiscreteInputs),
		holdingRegisters: make([]uint16, cfg.HoldingRegisters),
		inputRegisters:   make([]uint16, cfg.InputRegisters),
	}
	s.units[unit] = created
	return created
}

// unitsSnapshot returns the units that exist now. A broadcast iterates this
// snapshot, so a unit created after the broadcast does not receive it.
func (s *MemoryStore) unitsSnapshot() []*UnitStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*UnitStore, 0, len(s.units))
	for _, u := range s.units {
		out = append(out, u)
	}
	return out
}

// Handle implements the standard function codes against the store. An unknown
// function code returns ErrUnhandled so the server can answer IllegalFunction.
func (s *MemoryStore) Handle(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if unit == 0 {
		return s.handleBroadcast(req)
	}
	return s.handleUnit(s.unitFor(unit), req)
}

// handleBroadcast applies a broadcast write to every existing unit. A broadcast
// read is ignored, as the specification requires.
func (s *MemoryStore) handleBroadcast(req modbus.Message) (modbus.Message, error) {
	if !isWrite(req) {
		return nil, nil
	}
	for _, u := range s.unitsSnapshot() {
		// A broadcast has no response, so a unit that is too small simply ignores
		// the write instead of failing the frame.
		_, _ = u.handle(req, s.emit)
	}
	return nil, nil
}

// handleUnit runs one request against one unit.
func (s *MemoryStore) handleUnit(u *UnitStore, req modbus.Message) (modbus.Message, error) {
	return u.handle(req, s.emit)
}

// emit reports one write outside the store lock.
func (s *MemoryStore) emit(event WriteEvent) {
	s.mu.RLock()
	fn := s.onWrite
	s.mu.RUnlock()
	if fn != nil {
		fn(event)
	}
}

// UnitStore is one unit's four banks. Its methods are safe for concurrent use;
// the server serializes requests per connection, but a caller may also read or
// seed values from another goroutine.
type UnitStore struct {
	unit uint8

	mu               sync.RWMutex
	coils            []bool
	discreteInputs   []bool
	holdingRegisters []uint16
	inputRegisters   []uint16
}

// Unit returns the unit id this store covers.
func (u *UnitStore) Unit() uint8 { return u.unit }

// handle executes one request and reports writes through emit.
func (u *UnitStore) handle(req modbus.Message, emit func(WriteEvent)) (modbus.Message, error) {
	switch r := req.(type) {
	case *modbus.ReadCoilsRequest:
		values, err := u.Coils(r.Address, r.Quantity)
		if err != nil {
			return nil, err
		}
		return &modbus.ReadCoilsResponse{Bits: values}, nil
	case *modbus.ReadDiscreteInputsRequest:
		values, err := u.DiscreteInputs(r.Address, r.Quantity)
		if err != nil {
			return nil, err
		}
		return &modbus.ReadDiscreteInputsResponse{Bits: values}, nil
	case *modbus.ReadHoldingRegistersRequest:
		values, err := u.HoldingRegisters(r.Address, r.Quantity)
		if err != nil {
			return nil, err
		}
		return &modbus.ReadHoldingRegistersResponse{Registers: values}, nil
	case *modbus.ReadInputRegistersRequest:
		values, err := u.InputRegisters(r.Address, r.Quantity)
		if err != nil {
			return nil, err
		}
		return &modbus.ReadInputRegistersResponse{Registers: values}, nil
	case *modbus.WriteSingleCoil:
		if err := u.SetCoil(r.Address, r.Value); err != nil {
			return nil, err
		}
		emit(WriteEvent{Unit: u.unit, Kind: Coils, Address: r.Address, Coils: []bool{r.Value}})
		return &modbus.WriteSingleCoil{Address: r.Address, Value: r.Value}, nil
	case *modbus.WriteSingleRegister:
		if err := u.SetHoldingRegister(r.Address, r.Value); err != nil {
			return nil, err
		}
		emit(WriteEvent{Unit: u.unit, Kind: HoldingRegisters, Address: r.Address, Registers: []uint16{r.Value}})
		return &modbus.WriteSingleRegister{Address: r.Address, Value: r.Value}, nil
	case *modbus.WriteMultipleCoilsRequest:
		if err := u.SetCoils(r.Address, r.Values); err != nil {
			return nil, err
		}
		emit(WriteEvent{Unit: u.unit, Kind: Coils, Address: r.Address, Coils: append([]bool(nil), r.Values...)})
		return &modbus.WriteMultipleCoilsResponse{Address: r.Address, Quantity: uint16(len(r.Values))}, nil
	case *modbus.WriteMultipleRegistersRequest:
		if err := u.SetHoldingRegisters(r.Address, r.Values); err != nil {
			return nil, err
		}
		emit(WriteEvent{Unit: u.unit, Kind: HoldingRegisters, Address: r.Address, Registers: append([]uint16(nil), r.Values...)})
		return &modbus.WriteMultipleRegistersResponse{Address: r.Address, Quantity: uint16(len(r.Values))}, nil
	case *modbus.MaskWriteRegister:
		echo, value, err := u.MaskWrite(r.Address, r.AndMask, r.OrMask)
		if err != nil {
			return nil, err
		}
		emit(WriteEvent{Unit: u.unit, Kind: HoldingRegisters, Address: r.Address, Registers: []uint16{value}})
		return &echo, nil
	case *modbus.ReadWriteMultipleRegistersRequest:
		values, err := u.ReadWrite(r.ReadAddress, r.ReadQuantity, r.WriteAddress, r.WriteValues)
		if err != nil {
			return nil, err
		}
		emit(WriteEvent{Unit: u.unit, Kind: HoldingRegisters, Address: r.WriteAddress, Registers: append([]uint16(nil), r.WriteValues...)})
		return &modbus.ReadWriteMultipleRegistersResponse{Registers: values}, nil
	default:
		return nil, ErrUnhandled
	}
}

// Coils returns quantity coils starting at address.
func (u *UnitStore) Coils(address, quantity uint16) ([]bool, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if !inRange(address, quantity, len(u.coils)) {
		return nil, ErrOutOfRange
	}
	return append([]bool(nil), u.coils[address:int(address)+int(quantity)]...), nil
}

// Coil returns one coil.
func (u *UnitStore) Coil(address uint16) (bool, error) {
	values, err := u.Coils(address, 1)
	if err != nil {
		return false, err
	}
	return values[0], nil
}

// SetCoils writes values starting at address.
func (u *UnitStore) SetCoils(address uint16, values []bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !quantityFits(len(values)) || !inRange(address, uint16(len(values)), len(u.coils)) {
		return ErrOutOfRange
	}
	copy(u.coils[address:], values)
	return nil
}

// SetCoil writes one coil.
func (u *UnitStore) SetCoil(address uint16, value bool) error {
	return u.SetCoils(address, []bool{value})
}

// DiscreteInputs returns quantity discrete inputs starting at address.
func (u *UnitStore) DiscreteInputs(address, quantity uint16) ([]bool, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if !inRange(address, quantity, len(u.discreteInputs)) {
		return nil, ErrOutOfRange
	}
	return append([]bool(nil), u.discreteInputs[address:int(address)+int(quantity)]...), nil
}

// SetDiscreteInputs writes values starting at address.
func (u *UnitStore) SetDiscreteInputs(address uint16, values []bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !quantityFits(len(values)) || !inRange(address, uint16(len(values)), len(u.discreteInputs)) {
		return ErrOutOfRange
	}
	copy(u.discreteInputs[address:], values)
	return nil
}

// HoldingRegisters returns quantity holding registers starting at address.
func (u *UnitStore) HoldingRegisters(address, quantity uint16) ([]uint16, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if !inRange(address, quantity, len(u.holdingRegisters)) {
		return nil, ErrOutOfRange
	}
	return append([]uint16(nil), u.holdingRegisters[address:int(address)+int(quantity)]...), nil
}

// HoldingRegister returns one holding register.
func (u *UnitStore) HoldingRegister(address uint16) (uint16, error) {
	values, err := u.HoldingRegisters(address, 1)
	if err != nil {
		return 0, err
	}
	return values[0], nil
}

// SetHoldingRegisters writes values starting at address.
func (u *UnitStore) SetHoldingRegisters(address uint16, values []uint16) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !quantityFits(len(values)) || !inRange(address, uint16(len(values)), len(u.holdingRegisters)) {
		return ErrOutOfRange
	}
	copy(u.holdingRegisters[address:], values)
	return nil
}

// SetHoldingRegister writes one holding register.
func (u *UnitStore) SetHoldingRegister(address, value uint16) error {
	return u.SetHoldingRegisters(address, []uint16{value})
}

// InputRegisters returns quantity input registers starting at address.
func (u *UnitStore) InputRegisters(address, quantity uint16) ([]uint16, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if !inRange(address, quantity, len(u.inputRegisters)) {
		return nil, ErrOutOfRange
	}
	return append([]uint16(nil), u.inputRegisters[address:int(address)+int(quantity)]...), nil
}

// SetInputRegisters writes values starting at address.
func (u *UnitStore) SetInputRegisters(address uint16, values []uint16) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !quantityFits(len(values)) || !inRange(address, uint16(len(values)), len(u.inputRegisters)) {
		return ErrOutOfRange
	}
	copy(u.inputRegisters[address:], values)
	return nil
}

// MaskWrite applies the mask-write formula to one holding register and returns
// the echo fields and the resulting value.
func (u *UnitStore) MaskWrite(address, andMask, orMask uint16) (modbus.MaskWriteRegister, uint16, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !inRange(address, 1, len(u.holdingRegisters)) {
		return modbus.MaskWriteRegister{}, 0, ErrOutOfRange
	}
	current := u.holdingRegisters[address]
	next := (current & andMask) | (orMask &^ andMask)
	u.holdingRegisters[address] = next
	return modbus.MaskWriteRegister{Address: address, AndMask: andMask, OrMask: orMask}, next, nil
}

// ReadWrite writes values at writeAddress and reads readQuantity registers from
// readAddress in one atomic step, which is what function 0x17 defines.
func (u *UnitStore) ReadWrite(readAddress, readQuantity, writeAddress uint16, values []uint16) ([]uint16, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !inRange(writeAddress, uint16(len(values)), len(u.holdingRegisters)) {
		return nil, ErrOutOfRange
	}
	if !inRange(readAddress, readQuantity, len(u.holdingRegisters)) {
		return nil, ErrOutOfRange
	}
	copy(u.holdingRegisters[writeAddress:], values)
	return append([]uint16(nil), u.holdingRegisters[readAddress:int(readAddress)+int(readQuantity)]...), nil
}

// quantityFits reports whether a value count fits the uint16 quantity space.
func quantityFits(n int) bool { return n >= 0 && n <= 0xffff }

// inRange reports whether address..address+quantity-1 fits in a bank of size.
func inRange(address, quantity uint16, size int) bool {
	if quantity == 0 {
		return false
	}
	return int(address)+int(quantity) <= size
}

// isWrite reports whether req changes a bank.
func isWrite(req modbus.Message) bool {
	switch req.(type) {
	case modbus.WriteSingleCoil, *modbus.WriteSingleCoil,
		modbus.WriteSingleRegister, *modbus.WriteSingleRegister,
		modbus.MaskWriteRegister, *modbus.MaskWriteRegister,
		*modbus.WriteMultipleCoilsRequest, *modbus.WriteMultipleRegistersRequest,
		*modbus.ReadWriteMultipleRegistersRequest:
		return true
	default:
		return false
	}
}

// DeviceIdentification is the device identity a server reports for function
// code 0x2B/0x0E. Empty fields are left out of the object list.
type DeviceIdentification struct {
	VendorName          string
	ProductCode         string
	MajorMinorRevision  string
	VendorURL           string
	ProductName         string
	ModelName           string
	UserApplicationName string
}

// objects returns the objects selected by a read code, in object-id order.
func (d *DeviceIdentification) objects(readCode, objectID byte) []modbus.DeviceObject {
	all := []modbus.DeviceObject{
		{ID: modbus.DeviceIDVendorName, Value: []byte(d.VendorName)},
		{ID: modbus.DeviceIDProductCode, Value: []byte(d.ProductCode)},
		{ID: modbus.DeviceIDMajorMinorRevision, Value: []byte(d.MajorMinorRevision)},
		{ID: modbus.DeviceIDVendorURL, Value: []byte(d.VendorURL)},
		{ID: modbus.DeviceIDProductName, Value: []byte(d.ProductName)},
		{ID: modbus.DeviceIDModelName, Value: []byte(d.ModelName)},
		{ID: modbus.DeviceIDUserApplicationName, Value: []byte(d.UserApplicationName)},
	}
	limit := len(all)
	switch readCode {
	case modbus.DeviceIDBasic:
		limit = 3
	case modbus.DeviceIDRegular:
		limit = 6
	case modbus.DeviceIDExtended:
		limit = 7
	case modbus.DeviceIDIndividual:
		for _, object := range all {
			if object.ID == objectID && len(object.Value) > 0 {
				return []modbus.DeviceObject{object}
			}
		}
		return nil
	}
	out := make([]modbus.DeviceObject, 0, limit)
	for _, object := range all[:limit] {
		if len(object.Value) > 0 {
			out = append(out, object)
		}
	}
	return out
}

// handle builds one device identification response, splitting the object list
// across responses when it does not fit into maxPDU.
func (d *DeviceIdentification) handle(req *modbus.ReadDeviceIdentificationRequest, maxPDU int) (modbus.Message, error) {
	objects := d.objects(req.ReadCode, req.ObjectID)
	if len(objects) == 0 {
		return nil, &modbus.Exception{FunctionCode: modbus.FuncEncapsulatedInterface, Code: modbus.ExceptionIllegalDataAddress}
	}
	const fixed = 7
	remaining := maxPDU - fixed
	resp := &modbus.ReadDeviceIdentificationResponse{
		ReadCode:        req.ReadCode,
		ConformityLevel: 0x83,
	}
	for i, object := range objects {
		need := 2 + len(object.Value)
		if need > remaining {
			if i == 0 {
				return nil, &modbus.Exception{FunctionCode: modbus.FuncEncapsulatedInterface, Code: modbus.ExceptionIllegalDataAddress}
			}
			resp.MoreFollows = true
			resp.NextObjectID = object.ID
			break
		}
		resp.Objects = append(resp.Objects, object)
		remaining -= need
	}
	return resp, nil
}
