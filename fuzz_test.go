package gmodbus

import (
	"bufio"
	"bytes"
	"context"
	"testing"

	"github.com/maxid/gnety/codec/modbus"
)

// The two parser entry points a Modbus peer controls are the application data
// unit (what a master receives, and what a slave accepts) and the PDU inside it.
// The codec's own fuzz targets in gnety cover byte-to-frame decoding; these
// cover what this package adds on top: the pending-request match, the function
// check, the response decode, and the server's dispatch into the store.
//
// Neither target asserts that an input is accepted — a rejected frame is as
// interesting as an accepted one. They assert the invariants: no panic, a
// rejected frame never yields a message, an accepted frame survives the trip
// back to bytes unchanged, and every server response answers the request it was
// given.

// fuzzReadPending is the read every ADU seed is built around: one holding
// register, so a successful response has exactly one register.
func fuzzReadPending() modbus.Message {
	return &modbus.ReadHoldingRegistersRequest{Address: 0, Quantity: 1}
}

// fuzzPendings are the requests the ADU decoder is set up to answer, so the
// target covers a response with a payload and both acknowledgement forms: a
// read, a single write and a multiple write. Each answer has a canonical
// encoding, which is what makes the idempotence check meaningful.
func fuzzPendings() []modbus.Message {
	return []modbus.Message{
		fuzzReadPending(),
		&modbus.WriteSingleRegister{Address: 2, Value: 0x1234},
		&modbus.WriteMultipleCoilsRequest{Address: 4, Values: []bool{true, false, true}},
	}
}

// newFuzzStreamCodec returns a client stream codec with pending awaiting a
// response. Both framings are exercised: MBAP for the network transports and the
// CRC-framed RTU form for the serial family.
func newFuzzStreamCodec(rtuMode bool, pending modbus.Message) *streamCodec {
	codec := &streamCodec{rtuMode: rtuMode, maxPDU: modbus.MaxPDU}
	codec.setPending(1, 7, pending)
	return codec
}

// mustRTU frames a PDU as a CRC-framed RTU frame, for the seed corpus.
func mustRTU(f *testing.F, pdu []byte) []byte {
	f.Helper()
	raw, err := modbus.RTUOptions{}.Marshal(modbus.RTUFrame{UnitID: 1, PDU: pdu})
	if err != nil {
		f.Fatalf("seed the RTU frame %x: %v", pdu, err)
	}
	return raw
}

// FuzzDecodeADU feeds arbitrary bytes to the client's stream decoder in both
// framings.
func FuzzDecodeADU(f *testing.F) {
	// MBAP: a read answer, its exception form, and shapes a peer can send wrong.
	f.Add([]byte{0x00, 0x07, 0x00, 0x00, 0x00, 0x05, 0x01, 0x03, 0x02, 0x12, 0x34})
	f.Add([]byte{0x00, 0x07, 0x00, 0x00, 0x00, 0x03, 0x01, 0x83, 0x02})
	f.Add([]byte{0x00, 0x07, 0x00, 0x00, 0x00, 0x05, 0x01, 0x03, 0x02})
	f.Add([]byte{0x00, 0x07, 0x00, 0x01, 0x00, 0x03, 0x01, 0x03, 0x02})
	f.Add([]byte{0x00, 0x07, 0x00, 0x00, 0xff, 0xff, 0x01, 0x03})
	f.Add([]byte{0x00, 0x08, 0x00, 0x00, 0x00, 0x05, 0x01, 0x03, 0x02, 0x12, 0x34})
	f.Add([]byte{0x00, 0x07, 0x00, 0x00, 0x00, 0x05, 0x02, 0x03, 0x02, 0x12, 0x34})
	// The acknowledgement forms: a write-single echo and a write-multiple ack,
	// which carry no payload and are checked against the pending request instead.
	f.Add([]byte{0x00, 0x07, 0x00, 0x00, 0x00, 0x06, 0x01, 0x06, 0x00, 0x02, 0x12, 0x34})
	f.Add([]byte{0x00, 0x07, 0x00, 0x00, 0x00, 0x06, 0x01, 0x0f, 0x00, 0x04, 0x00, 0x03})

	// RTU: the same answers with a valid CRC16, the same answer with a corrupted
	// checksum, and a truncated frame.
	valid := mustRTU(f, []byte{0x03, 0x02, 0x12, 0x34})
	f.Add(valid)
	f.Add(valid[:len(valid)-3])
	corrupt := append([]byte(nil), valid...)
	corrupt[len(corrupt)-1] ^= 0xff
	f.Add(corrupt)
	f.Add(mustRTU(f, []byte{0x83, 0x02}))
	f.Add(mustRTU(f, []byte{0x06, 0x00, 0x02, 0x12, 0x34}))
	f.Add(mustRTU(f, []byte{0x0f, 0x00, 0x04, 0x00, 0x03}))
	f.Add([]byte{})
	f.Add([]byte{0x01})

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, pending := range fuzzPendings() {
			for _, rtuMode := range []bool{false, true} {
				codec := newFuzzStreamCodec(rtuMode, pending)
				reader := bufio.NewReader(bytes.NewReader(data))
				value, err := codec.Decode(reader)
				if err != nil {
					// A rejected frame must not also produce a message.
					if value != nil {
						t.Fatalf("%T rtu=%v: Decode returned %v with error %v", pending, rtuMode, value, err)
					}
					continue
				}
				msg, ok := value.(modbus.Message)
				if !ok || msg == nil {
					t.Fatalf("%T rtu=%v: Decode returned %T, not a modbus message", pending, rtuMode, value)
				}
				// The only answers it may accept are the pending request's own
				// function code and its exception form.
				want := pending.Function()
				if got := msg.Function(); got != want && got != want|0x80 {
					t.Fatalf("%T rtu=%v: decoded function %#02x for a %#02x request", pending, rtuMode, got, want)
				}
				// A read answer carries exactly the requested quantity: a peer cannot
				// make the decoder allocate more than the request allows.
				if read, isRead := msg.(*modbus.ReadHoldingRegistersResponse); isRead && len(read.Registers) != 1 {
					t.Fatalf("%T rtu=%v: a one-register read decoded %d registers", pending, rtuMode, len(read.Registers))
				}
				// Idempotence: encoding the message again reproduces the frame that
				// was consumed, byte for byte. Decoding is canonicalizing, never lossy.
				var pdu bytes.Buffer
				if err := modbus.EncodePDU(&pdu, msg); err != nil {
					t.Fatalf("%T rtu=%v: re-encode %T: %v", pending, rtuMode, msg, err)
				}
				frame, err := codec.encode(clientFrame{unit: 1, txID: 7, pdu: pdu.Bytes()})
				if err != nil {
					t.Fatalf("%T rtu=%v: re-frame %T: %v", pending, rtuMode, msg, err)
				}
				consumed := len(data) - reader.Buffered()
				if !bytes.Equal(frame, data[:consumed]) {
					t.Fatalf("%T rtu=%v: decoded %T re-encodes to %x, the frame was %x",
						pending, rtuMode, msg, frame, data[:consumed])
				}
			}
		}
	})
}

// FuzzDecodePDU feeds arbitrary PDUs to the server: decode, then dispatch into
// the store, the device identification and the exception mapping.
func FuzzDecodePDU(f *testing.F) {
	// One seed per modeled request function code.
	f.Add([]byte{0x01, 0x00, 0x00, 0x00, 0x08})                                                       // read coils
	f.Add([]byte{0x02, 0x00, 0x00, 0x00, 0x08})                                                       // read discrete inputs
	f.Add([]byte{0x03, 0x00, 0x00, 0x00, 0x04})                                                       // read holding registers
	f.Add([]byte{0x04, 0x00, 0x00, 0x00, 0x04})                                                       // read input registers
	f.Add([]byte{0x05, 0x00, 0x00, 0xff, 0x00})                                                       // write single coil
	f.Add([]byte{0x06, 0x00, 0x01, 0x12, 0x34})                                                       // write single register
	f.Add([]byte{0x0f, 0x00, 0x00, 0x00, 0x03, 0x01, 0x05})                                           // write multiple coils
	f.Add([]byte{0x10, 0x00, 0x00, 0x00, 0x02, 0x04, 0x00, 0x0a, 0x01, 0x02})                         // write multiple registers
	f.Add([]byte{0x16, 0x00, 0x04, 0x00, 0xf2, 0x00, 0x25})                                           // mask write
	f.Add([]byte{0x17, 0x00, 0x00, 0x00, 0x01, 0x00, 0x10, 0x00, 0x02, 0x04, 0x00, 0x07, 0x00, 0x08}) // read/write multiple
	f.Add([]byte{0x2b, 0x0e, 0x01, 0x00})                                                             // read device identification
	// The boundaries: an out-of-range range, a zero quantity, a byte count that
	// disagrees with the quantity, an unknown function code, a truncated request
	// and an exception form sent as a request.
	f.Add([]byte{0x03, 0xff, 0xff, 0x00, 0x7d})
	f.Add([]byte{0x03, 0x00, 0x00, 0x00, 0x00})
	f.Add([]byte{0x10, 0x00, 0x00, 0x00, 0x02, 0x02, 0x00, 0x0a})
	f.Add([]byte{0x41, 0x00, 0x01, 0x02, 0x03})
	f.Add([]byte{0x03})
	f.Add([]byte{0x83, 0x02})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, pdu []byte) {
		for _, network := range []Network{NetworkTCP, NetworkRTU} {
			srv := fuzzServer(network)
			req := serverRequest{unit: 1, function: pduFunction(pdu)}
			req.msg, req.decodeErr = srv.functions.DecodePDU(pdu)
			if req.decodeErr != nil && req.msg != nil {
				t.Fatalf("network=%s: a rejected PDU also decoded to %T", network, req.msg)
			}

			resp, respond := srv.handleServerRequest(req)
			if !respond {
				if resp != nil {
					t.Fatalf("network=%s: %x answers nothing but returned %T", network, pdu, resp)
				}
				continue
			}
			if resp == nil {
				t.Fatalf("network=%s: %x must be answered but produced no message", network, pdu)
			}
			// Every answer either carries the request's function code or its
			// exception form, whatever the PDU looked like.
			if function := resp.Function(); function != req.function && !modbus.IsException(function) {
				t.Fatalf("network=%s: %x answered with function %#02x", network, pdu, function)
			}
			if _, err := encodeMessage(resp); err != nil {
				t.Fatalf("network=%s: %x answered with an unencodable %T: %v", network, pdu, resp, err)
			}
		}
	})
}

// fuzzServer is a server without a socket: the dispatch path a fuzz target
// exercises needs the store, the device identification and a live close context,
// and none of those needs a listening transport.
func fuzzServer(network Network) *Server {
	store := NewMemoryStore(nil)
	store.AddUnit(1, UnitConfig{Coils: 16, DiscreteInputs: 16, HoldingRegisters: 16, InputRegisters: 16})
	return &Server{
		network:  network,
		store:    store,
		maxPDU:   modbus.MaxPDU,
		closeCtx: context.Background(),
		device:   &DeviceIdentification{VendorName: "gmodbus fuzz", ProductCode: "FUZZ-1"},
	}
}

// pduFunction is the function code a PDU starts with, or 0 for an empty one.
func pduFunction(pdu []byte) byte {
	if len(pdu) == 0 {
		return 0
	}
	return pdu[0]
}

// TestADURefusesCorruptedFraming pins the checks and bounds the fuzz targets
// reach only by chance: a checksum mismatch and a length the peer declares but
// does not send are refused, and a declared length above MaxPDU is refused
// before anything is allocated.
func TestADURefusesCorruptedFraming(t *testing.T) {
	answer := modbus.RTUFrame{UnitID: 1, PDU: []byte{0x03, 0x02, 0x12, 0x34}}
	valid, err := modbus.RTUOptions{}.Marshal(answer)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"checksum": func(frame []byte) []byte {
			frame[len(frame)-1] ^= 0xff
			return frame
		},
		"payload": func(frame []byte) []byte {
			frame[1] ^= 0x01
			return frame
		},
		"truncated": func(frame []byte) []byte { return frame[:len(frame)-2] },
	} {
		t.Run("rtu "+name, func(t *testing.T) {
			frame := mutate(append([]byte(nil), valid...))
			if _, err := newFuzzStreamCodec(true, fuzzReadPending()).Decode(bufio.NewReader(bytes.NewReader(frame))); err == nil {
				t.Fatalf("a %s-corrupted RTU frame was accepted: %x", name, frame)
			}
		})
	}

	// A peer that declares more than MaxPDU must be refused without the reader
	// reserving the declared size.
	huge := []byte{0x00, 0x07, 0x00, 0x00, 0xff, 0xff, 0x01, 0x03, 0x02}
	if _, err := newFuzzStreamCodec(false, fuzzReadPending()).Decode(bufio.NewReader(bytes.NewReader(huge))); err == nil {
		t.Fatal("an MBAP frame declaring 65535 bytes was accepted")
	}
	// A peer that ends the stream mid-frame is a disconnect, not a message.
	short := []byte{0x00, 0x07, 0x00, 0x00, 0x00, 0x05, 0x01, 0x03, 0x02}
	if _, err := newFuzzStreamCodec(false, fuzzReadPending()).Decode(bufio.NewReader(bytes.NewReader(short))); err == nil {
		t.Fatal("an MBAP frame that ends mid-PDU was accepted")
	}
}
