package gmodbus

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/maxid/gnety"
	"github.com/maxid/gnety/codec/modbus"
	"github.com/maxid/gnety/codec/wire"
)

// serverRequest is one decoded inbound frame. decodeErr reports a PDU the codec
// rejected while the frame itself was well formed; the server answers it with
// an exception instead of dropping the connection.
type serverRequest struct {
	unit      uint8
	txID      uint16
	function  byte
	msg       modbus.Message
	decodeErr error
}

// serverResponse is what the datagram handler hands back to gnety for encoding.
type serverResponse struct {
	unit uint8
	txID uint16
	msg  modbus.Message
}

// serveStream is the gnety RawHandler for tcp, tls and rtu-tcp. One connection
// carries one request at a time and is answered in order.
func (s *Server) serveStream(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		if s.idleTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.idleTimeout))
		} else {
			_ = conn.SetReadDeadline(time.Time{})
		}
		req, err := s.readStreamRequest(reader)
		if err != nil {
			if !s.isClosed() && !isTimeout(err) {
				s.reportError(fmt.Errorf("gmodbus: read request: %w", err))
			}
			return
		}
		resp, respond := s.handleServerRequest(req)
		if !respond || resp == nil {
			continue
		}
		raw, err := s.encodeStreamResponse(req, resp)
		if err != nil {
			s.reportError(fmt.Errorf("gmodbus: encode response: %w", err))
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(DefaultTimeout))
		if err := writeAll(conn, raw); err != nil {
			if !s.isClosed() {
				s.reportError(fmt.Errorf("gmodbus: write response: %w", err))
			}
			return
		}
	}
}

// serveSerial runs the RTU serial transport. The port is exclusive, so one
// request is read, handled and answered before the next one.
func (s *Server) serveSerial() {
	defer s.wg.Done()
	reader := bufio.NewReader(s.serial)
	for {
		req, err := s.readStreamRequest(reader)
		if err != nil {
			if s.isClosed() {
				return
			}
			s.reportError(fmt.Errorf("gmodbus: read serial request: %w", err))
			// A port that fails every read (unplugged adapter, a closed handle
			// the driver does not report as such) must not turn into a busy loop.
			if !s.waitSerialRetry() {
				return
			}
			continue
		}
		resp, respond := s.handleServerRequest(req)
		if !respond || resp == nil {
			continue
		}
		raw, err := s.encodeStreamResponse(req, resp)
		if err != nil {
			s.reportError(fmt.Errorf("gmodbus: encode serial response: %w", err))
			continue
		}
		if err := writeAll(s.serial, raw); err != nil {
			if s.isClosed() {
				return
			}
			s.reportError(fmt.Errorf("gmodbus: write serial response: %w", err))
		}
	}
}

// readStreamRequest reads and decodes one MBAP or RTU frame.
func (s *Server) readStreamRequest(r *bufio.Reader) (serverRequest, error) {
	if s.network.rtuFamily() {
		frame, err := s.rtu.Read(r, s.maxPDU, s.functions)
		if err != nil {
			return serverRequest{}, err
		}
		req := serverRequest{unit: frame.UnitID, function: frame.Function()}
		req.msg, req.decodeErr = s.functions.DecodePDU(frame.PDU)
		return req, nil
	}
	frame, err := modbus.Read(r, s.maxPDU)
	if err != nil {
		return serverRequest{}, err
	}
	req := serverRequest{unit: frame.UnitID, txID: frame.TransactionID, function: frame.Function()}
	req.msg, req.decodeErr = s.functions.DecodePDU(frame.PDU)
	return req, nil
}

// waitSerialRetry waits out one serial read failure. It reports false when the
// server closed while waiting.
func (s *Server) waitSerialRetry() bool {
	timer := time.NewTimer(serialRetryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-s.closeCtx.Done():
		return false
	}
}

// serialRetryDelay spaces out consecutive serial read failures, so a broken
// port costs a retry every 100ms instead of a busy loop.
const serialRetryDelay = 100 * time.Millisecond

// handleServerRequest turns one decoded frame into a response. A PDU the codec
// rejected becomes an exception when the function code was readable.
func (s *Server) handleServerRequest(req serverRequest) (modbus.Message, bool) {
	if req.decodeErr != nil {
		code := modbus.ExceptionIllegalDataValue
		if wire.KindOf(req.decodeErr) == wire.KindUnsupported {
			code = modbus.ExceptionIllegalFunction
		}
		s.reportError(fmt.Errorf("gmodbus: decode request: %w", req.decodeErr))
		if s.metrics != nil {
			s.metrics.Request(req.function, ServerResultException, 0)
		}
		// A broadcast is never answered, not even to report the bad frame.
		return exceptionForFunction(req.function, code), !s.isBroadcast(req.unit)
	}
	if req.msg == nil {
		return nil, false
	}
	return s.handleRequest(req.unit, req.msg)
}

// isBroadcast reports whether a request is a broadcast on this transport. Only
// the RTU transports define unit 0 as a broadcast; MBAP treats it as a unit.
func (s *Server) isBroadcast(unit uint8) bool {
	return s.network.rtuFamily() && unit == 0
}

// encodeStreamResponse builds the wire frame for a response.
func (s *Server) encodeStreamResponse(req serverRequest, resp modbus.Message) ([]byte, error) {
	pdu, err := encodeMessage(resp)
	if err != nil {
		return nil, err
	}
	if s.network.rtuFamily() {
		return s.rtu.Marshal(modbus.RTUFrame{UnitID: req.unit, PDU: pdu})
	}
	return modbus.Frame{TransactionID: req.txID, UnitID: req.unit, PDU: pdu}.Marshal()
}

// encodeMessage encodes one response message as PDU bytes.
func encodeMessage(msg modbus.Message) ([]byte, error) {
	var buf bytes.Buffer
	if err := modbus.EncodePDU(&buf, msg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// exceptionForFunction builds an exception response for a raw function code,
// including a code the codec could not decode.
func exceptionForFunction(function byte, code modbus.ExceptionCode) *modbus.Exception {
	return &modbus.Exception{FunctionCode: function, Code: code}
}

// datagramHandler answers UDP and DTLS datagrams through gnety sessions.
func (s *Server) datagramHandler() gnety.Handler {
	return gnety.HandlerFuncs{Message: func(sess *gnety.Session, value any) {
		req, ok := value.(serverRequest)
		if !ok {
			return
		}
		resp, respond := s.handleServerRequest(req)
		if !respond || resp == nil {
			return
		}
		if err := sess.Send(serverResponse{unit: req.unit, txID: req.txID, msg: resp}); err != nil && !s.isClosed() {
			s.reportError(fmt.Errorf("gmodbus: write datagram response: %w", err))
		}
	}}
}

// serverDatagramCodec implements gnety.DatagramCodec for UDP, DTLS and RTU over
// UDP.
type serverDatagramCodec struct {
	s *Server
}

func (c serverDatagramCodec) Decode(data []byte) (any, error) {
	s := c.s
	if s.network.rtuFamily() {
		frame, err := s.rtu.Parse(data, s.maxPDU)
		if err != nil {
			return nil, err
		}
		req := serverRequest{unit: frame.UnitID, function: frame.Function()}
		req.msg, req.decodeErr = s.functions.DecodePDU(frame.PDU)
		return req, nil
	}
	frame, err := modbus.Parse(data, s.maxPDU)
	if err != nil {
		return nil, err
	}
	req := serverRequest{unit: frame.UnitID, txID: frame.TransactionID, function: frame.Function()}
	req.msg, req.decodeErr = s.functions.DecodePDU(frame.PDU)
	return req, nil
}

func (c serverDatagramCodec) Encode(value any) ([]byte, error) {
	resp, ok := value.(serverResponse)
	if !ok {
		return nil, fmt.Errorf("gmodbus: datagram codec expects a serverResponse, got %T", value)
	}
	pdu, err := encodeMessage(resp.msg)
	if err != nil {
		return nil, err
	}
	if c.s.network.rtuFamily() {
		return c.s.rtu.Marshal(modbus.RTUFrame{UnitID: resp.unit, PDU: pdu})
	}
	return modbus.Frame{TransactionID: resp.txID, UnitID: resp.unit, PDU: pdu}.Marshal()
}

// writeAll writes the whole frame. A stream write may be short, and the next
// request must start at a frame boundary.
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// isTimeout reports whether err is a deadline that expired.
func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
