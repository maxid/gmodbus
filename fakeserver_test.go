package gmodbus

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/maxid/gnety/codec/modbus"
	"github.com/maxid/gnety/codec/wire"
)

// fakeRequest is one request the fake server decoded, with the connection it
// arrived on and its position in the overall request sequence.
type fakeRequest struct {
	// index is the zero-based position in the sequence of all requests.
	index int
	// conn is the 1-based index of the connection it arrived on.
	conn int
	// tx and unit are the identifiers the client chose.
	tx   uint16
	unit uint8
	// function is the request function code.
	function byte
	// msg is the decoded request PDU.
	msg modbus.Message
}

// fakeServer is an in-process Modbus TCP server built on the codec. It decodes
// every request, records it, and answers through a pluggable handler so a test
// can stall, answer with an exception, or answer the wrong transaction.
type fakeServer struct {
	t  *testing.T
	ln net.Listener

	mu       sync.Mutex
	closed   bool
	requests []fakeRequest
	conns    []net.Conn
	connSeq  int
	respond  func(*fakeServer, fakeRequest) *modbus.Frame
	drop     func(fakeRequest) bool
	failures []string
	// probePipelining makes a connection check, before answering, whether a
	// second request already arrived. A client with one request in flight never
	// pipelines.
	probePipelining bool
	wg              sync.WaitGroup
}

// newFakeServer starts a server on a loopback port and closes it at the end of
// the test.
func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeServer{t: t, ln: ln}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.close)
	return s
}

// addr is the address a client dials.
func (s *fakeServer) addr() string { return s.ln.Addr().String() }

// onRequest installs the response handler. It runs on the connection goroutine
// and returns the frame to send; nil sends nothing, which stalls the client.
func (s *fakeServer) onRequest(fn func(*fakeServer, fakeRequest) *modbus.Frame) {
	s.mu.Lock()
	s.respond = fn
	s.mu.Unlock()
}

// setProbePipelining makes every connection check for a request that arrived
// before the previous one was answered.
func (s *fakeServer) setProbePipelining(on bool) {
	s.mu.Lock()
	s.probePipelining = on
	s.mu.Unlock()
}

// dropRequests makes the server close the connection without answering the
// requests match reports, which is how a transport failure is simulated.
func (s *fakeServer) dropRequests(match func(fakeRequest) bool) {
	s.mu.Lock()
	s.drop = match
	s.mu.Unlock()
}

// seen returns a copy of every request the server decoded so far.
func (s *fakeServer) seen() []fakeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fakeRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// count reports how many requests the server decoded so far.
func (s *fakeServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// close stops the listener, closes every accepted connection and waits for the
// goroutines. Failures recorded on the server goroutines are reported here,
// where the testing.T is still valid.
func (s *fakeServer) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()

	_ = s.ln.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	s.wg.Wait()

	s.mu.Lock()
	failures := s.failures
	s.failures = nil
	s.mu.Unlock()
	for _, failure := range failures {
		s.t.Errorf("fake server: %s", failure)
	}
}

// fail records a problem for close to report on the test goroutine.
func (s *fakeServer) fail(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, fmt.Sprintf(format, args...))
}

func (s *fakeServer) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.connSeq++
		index := s.connSeq
		s.conns = append(s.conns, conn)
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn, index)
	}
}

// serve reads frames from one connection until it ends.
func (s *fakeServer) serve(conn net.Conn, index int) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	for {
		frame, err := modbus.Read(r, modbus.MaxPDU)
		if err != nil {
			// An incomplete frame (the client closed or reset mid-frame) ends the
			// loop quietly; a classified wire error is a malformed stream worth
			// reporting.
			if kind := wire.KindOf(err); kind != wire.KindUnknown && kind != wire.KindIncomplete {
				s.fail("connection %d: read request: %v", index, err)
			}
			return
		}
		msg, err := modbus.DecodePDU(frame.PDU)
		if err != nil {
			s.fail("connection %d: decode request: %v", index, err)
			return
		}

		s.mu.Lock()
		req := fakeRequest{
			index:    len(s.requests),
			conn:     index,
			tx:       frame.TransactionID,
			unit:     frame.UnitID,
			function: frame.Function(),
			msg:      msg,
		}
		s.requests = append(s.requests, req)
		respond := s.respond
		probe := s.probePipelining
		drop := s.drop
		s.mu.Unlock()

		if drop != nil && drop(req) {
			// Abandon the connection mid-transaction: the client must treat it as
			// a transport failure and never reuse it.
			return
		}

		if probe && s.pipelined(r, conn, index, req.index) {
			return
		}
		if respond == nil {
			respond = cannedResponse
		}
		reply := respond(s, req)
		if reply == nil {
			// No answer: the client's attempt times out. Keep reading so a
			// reconnect test can still observe the connection ending.
			continue
		}
		buf, err := reply.Marshal()
		if err != nil {
			s.fail("connection %d: marshal response: %v", index, err)
			return
		}
		if _, err := conn.Write(buf); err != nil {
			return
		}
	}
}

// pipelined reports whether a second request arrived before req was answered.
func (s *fakeServer) pipelined(r *bufio.Reader, conn net.Conn, connIndex, reqIndex int) bool {
	deadline := time.Now().Add(20 * time.Millisecond)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return false
	}
	_, err := r.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return false
	}
	s.fail("connection %d: request %d was answered after a later request already arrived", connIndex, reqIndex)
	return true
}

// cannedResponse answers a request with deterministic data derived from the
// request itself, so a test can check that each caller got its own answer.
func cannedResponse(_ *fakeServer, req fakeRequest) *modbus.Frame {
	var resp modbus.Message
	switch m := req.msg.(type) {
	case *modbus.ReadCoilsRequest:
		resp = modbus.ReadCoilsResponse{Bits: coilPattern(m.Address, int(m.Quantity))}
	case *modbus.ReadDiscreteInputsRequest:
		resp = modbus.ReadDiscreteInputsResponse{Bits: inputPattern(m.Address, int(m.Quantity))}
	case *modbus.ReadHoldingRegistersRequest:
		resp = modbus.ReadHoldingRegistersResponse{Registers: registerPattern(m.Address, int(m.Quantity), 1)}
	case *modbus.ReadInputRegistersRequest:
		resp = modbus.ReadInputRegistersResponse{Registers: registerPattern(m.Address, int(m.Quantity), 2)}
	case *modbus.WriteSingleCoil:
		// The 0x05 response is the request echoed unchanged.
		resp = *m
	case *modbus.WriteSingleRegister:
		resp = *m
	case *modbus.WriteMultipleCoilsRequest:
		resp = modbus.WriteMultipleCoilsResponse{Address: m.Address, Quantity: uint16(len(m.Values))}
	case *modbus.WriteMultipleRegistersRequest:
		resp = modbus.WriteMultipleRegistersResponse{Address: m.Address, Quantity: uint16(len(m.Values))}
	default:
		return nil
	}
	frame, err := modbus.TypedFrame{TransactionID: req.tx, UnitID: req.unit, Message: resp}.Frame()
	if err != nil {
		return nil
	}
	return &frame
}

// exceptionResponse builds an exception reply for a request.
func exceptionResponse(req fakeRequest, code modbus.ExceptionCode) *modbus.Frame {
	frame, err := modbus.TypedFrame{
		TransactionID: req.tx,
		UnitID:        req.unit,
		Message:       modbus.Exception{FunctionCode: req.function, Code: code},
	}.Frame()
	if err != nil {
		return nil
	}
	return &frame
}

// replyWith rewrites the transaction or unit id of a canned response.
func replyWith(req fakeRequest, tx uint16, unit uint8) *modbus.Frame {
	frame := cannedResponse(nil, req)
	if frame == nil {
		return nil
	}
	frame.TransactionID = tx
	frame.UnitID = unit
	return frame
}

// coilPattern is the coil answer for an address: a deterministic mix of on and
// off states that depends on the address.
func coilPattern(address uint16, n int) []bool {
	bits := make([]bool, n)
	for i := range bits {
		bits[i] = (int(address)+i)%2 == 0
	}
	return bits
}

// inputPattern is the discrete-input answer, the inverse of coilPattern.
func inputPattern(address uint16, n int) []bool {
	bits := make([]bool, n)
	for i := range bits {
		bits[i] = (int(address)+i)%2 != 0
	}
	return bits
}

// registerPattern is the register answer: address plus an index, scaled by
// scale so holding and input registers differ.
func registerPattern(address uint16, n int, scale uint16) []uint16 {
	regs := make([]uint16, n)
	for i := range regs {
		regs[i] = scale*address + uint16(i) + 1
	}
	return regs
}
