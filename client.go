// Package gmodbus is a Modbus TCP master: one TCP connection, typed requests,
// transaction matching, bounded retries, exception mapping and a polling
// runner.
//
// The wire format lives in github.com/maxid/gnety/codec/modbus. This package
// owns the connection and the exchange it carries and never re-encodes MBAP
// headers or PDUs itself.
//
// Scope is Modbus TCP master only. Serial framing, slave and server roles,
// register maps, device models and a command line tool are out of scope.
//
// # Concurrency
//
// A Modbus TCP connection has no out-of-order guarantee, so the client keeps one
// request in flight: every operation takes the same slot, and a second caller
// waits for the current exchange to finish or for its own context to end,
// whichever comes first. Operations are therefore safe to call from several
// goroutines, but they do not overlap on the wire.
//
// # Transactions and retries
//
// Each operation takes the next transaction id from the client's counter
// (wraparound tolerated) and rejects a response whose transaction id or unit id
// differs from the request (ErrTransactionMismatch).
//
// Timeout bounds one attempt. Retries is the number of extra attempts after the
// first, and only a timeout or a transport failure is retried: a Modbus
// exception response is a delivered answer and is returned immediately as
// *Error. A failed attempt closes its connection, so a retry dials again with
// DialTimeout.
//
// When attempts run out, or when a response is unusable, the client closes its
// connection for good: later calls fail fast with ErrClosed instead of looking
// healthy on a stream that may still carry a stale reply. Dial a new client
// (or replace the one a poller uses) to recover.
package gmodbus

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maxid/gnety/codec/modbus"
	"github.com/maxid/gnety/codec/wire"
)

// Defaults used when a Config field is zero.
const (
	// DefaultTimeout bounds one attempt, write plus read.
	DefaultTimeout = 5 * time.Second
	// DefaultDialTimeout bounds one TCP connect.
	DefaultDialTimeout = 5 * time.Second
)

// Config configures a Client. A zero timing field takes the package default; a
// negative one is rejected by Dial.
type Config struct {
	// Addr is the Modbus TCP server address, "host:port". Required.
	Addr string
	// DialTimeout bounds one TCP connect, including a reconnect between retries.
	// Zero means DefaultDialTimeout.
	DialTimeout time.Duration
	// Timeout bounds one attempt: the write and the matching read share it.
	// Zero means DefaultTimeout.
	Timeout time.Duration
	// Retries is how many extra attempts follow the first when an attempt times
	// out or hits a transport failure. Zero means a single attempt.
	Retries int
	// MaxPDU bounds an inbound PDU. Zero means modbus.MaxPDU; a larger value is
	// rejected.
	MaxPDU int
	// Metrics receives one event per attempt. Nil costs nothing, and the client
	// never calls it while holding its own lock.
	Metrics Metrics
}

// Client is one Modbus TCP master connection.
//
// A Client is safe for concurrent use; its operations are serialized on the
// wire. Close makes the client unusable, and a client whose exchange ran out of
// attempts is closed too.
type Client struct {
	addr        string
	timeout     time.Duration
	dialTimeout time.Duration
	retries     int
	maxPDU      int
	metrics     Metrics

	// nextTx hands out transaction ids. Wraparound is tolerated: a reused id
	// only matters for a reply that is still in flight, and the client never
	// leaves one behind on a live connection.
	nextTx atomic.Uint32

	// sem holds the single exchange slot. Sending into it acquires the slot,
	// receiving from it releases it, and a caller that cannot acquire it waits on
	// its own context.
	sem chan struct{}

	// mu guards the connection state. It is never held across network I/O.
	mu     sync.Mutex
	conn   net.Conn
	r      *bufio.Reader
	closed bool
}

// Dial connects to a Modbus TCP server and returns a client for it.
//
// Dial does no request: the connection is verified by the first operation.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, errors.New("gmodbus: Config.Addr is required")
	}
	if cfg.DialTimeout < 0 {
		return nil, errors.New("gmodbus: Config.DialTimeout is negative")
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("gmodbus: Config.Timeout is negative")
	}
	if cfg.Retries < 0 {
		return nil, errors.New("gmodbus: Config.Retries is negative")
	}
	if cfg.MaxPDU < 0 || cfg.MaxPDU > modbus.MaxPDU {
		return nil, fmt.Errorf("gmodbus: Config.MaxPDU %d is outside 0..%d", cfg.MaxPDU, modbus.MaxPDU)
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxPDU == 0 {
		cfg.MaxPDU = modbus.MaxPDU
	}
	conn, err := dialTCP(ctx, cfg.Addr, cfg.DialTimeout)
	if err != nil {
		return nil, err
	}
	return &Client{
		addr:        cfg.Addr,
		timeout:     cfg.Timeout,
		dialTimeout: cfg.DialTimeout,
		retries:     cfg.Retries,
		maxPDU:      cfg.MaxPDU,
		metrics:     cfg.Metrics,
		sem:         make(chan struct{}, 1),
		conn:        conn,
		r:           bufio.NewReader(conn),
	}, nil
}

// Addr returns the address the client was dialed with.
func (c *Client) Addr() string { return c.addr }

// Close closes the connection. It is idempotent: a second Close reports nil and
// leaves a client that is already closed untouched.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.conn
	c.conn, c.r = nil, nil
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// ReadCoils reads quantity coils starting at address (function code 0x01).
//
// The result holds one state per requested coil; element 0 is the coil at
// address. quantity must be in 1..modbus.MaxReadBits, otherwise the codec
// rejects the request before anything is sent.
func (c *Client) ReadCoils(ctx context.Context, unit uint8, address, quantity uint16) ([]bool, error) {
	msg, err := c.do(ctx, unit, &modbus.ReadCoilsRequest{Address: address, Quantity: quantity})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*modbus.ReadCoilsResponse)
	if !ok {
		return nil, unexpectedResponse("read coils", msg)
	}
	return resp.Bits, nil
}

// ReadDiscreteInputs reads quantity discrete inputs starting at address
// (function code 0x02). quantity must be in 1..modbus.MaxReadBits.
func (c *Client) ReadDiscreteInputs(ctx context.Context, unit uint8, address, quantity uint16) ([]bool, error) {
	msg, err := c.do(ctx, unit, &modbus.ReadDiscreteInputsRequest{Address: address, Quantity: quantity})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*modbus.ReadDiscreteInputsResponse)
	if !ok {
		return nil, unexpectedResponse("read discrete inputs", msg)
	}
	return resp.Bits, nil
}

// ReadHoldingRegisters reads quantity holding registers starting at address
// (function code 0x03). quantity must be in 1..modbus.MaxReadRegisters.
func (c *Client) ReadHoldingRegisters(ctx context.Context, unit uint8, address, quantity uint16) ([]uint16, error) {
	msg, err := c.do(ctx, unit, &modbus.ReadHoldingRegistersRequest{Address: address, Quantity: quantity})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*modbus.ReadHoldingRegistersResponse)
	if !ok {
		return nil, unexpectedResponse("read holding registers", msg)
	}
	return resp.Registers, nil
}

// ReadInputRegisters reads quantity input registers starting at address
// (function code 0x04). quantity must be in 1..modbus.MaxReadRegisters.
func (c *Client) ReadInputRegisters(ctx context.Context, unit uint8, address, quantity uint16) ([]uint16, error) {
	msg, err := c.do(ctx, unit, &modbus.ReadInputRegistersRequest{Address: address, Quantity: quantity})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*modbus.ReadInputRegistersResponse)
	if !ok {
		return nil, unexpectedResponse("read input registers", msg)
	}
	return resp.Registers, nil
}

// WriteSingleCoil writes one coil (function code 0x05). The server must echo the
// address and value, otherwise the codec rejects the response.
func (c *Client) WriteSingleCoil(ctx context.Context, unit uint8, address uint16, value bool) error {
	_, err := c.do(ctx, unit, modbus.WriteSingleCoil{Address: address, Value: value})
	return err
}

// WriteSingleRegister writes one holding register (function code 0x06). The
// server must echo the address and value.
func (c *Client) WriteSingleRegister(ctx context.Context, unit uint8, address, value uint16) error {
	_, err := c.do(ctx, unit, modbus.WriteSingleRegister{Address: address, Value: value})
	return err
}

// WriteMultipleCoils writes len(values) coils starting at address (function
// code 0x0F). The server must acknowledge the same address and quantity.
func (c *Client) WriteMultipleCoils(ctx context.Context, unit uint8, address uint16, values []bool) error {
	_, err := c.do(ctx, unit, &modbus.WriteMultipleCoilsRequest{Address: address, Values: values})
	return err
}

// WriteMultipleRegisters writes len(values) holding registers starting at
// address (function code 0x10). The server must acknowledge the same address
// and quantity.
func (c *Client) WriteMultipleRegisters(ctx context.Context, unit uint8, address uint16, values []uint16) error {
	_, err := c.do(ctx, unit, &modbus.WriteMultipleRegistersRequest{Address: address, Values: values})
	return err
}

// outcome classifies one attempt.
type outcome uint8

const (
	outcomeOK outcome = iota
	outcomeException
	outcomeTimeout
	outcomeTransport
	outcomeMismatch
	outcomeProtocol
	outcomeContext
	outcomeClosed
)

// do runs one transaction: validation, transaction id assignment, the
// serialized attempt loop and the mapping of the final result.
func (c *Client) do(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Validate the request through the codec before taking the exchange slot: a bad
	// address, quantity or value list costs no attempt and no round trip.
	if err := modbus.EncodePDU(io.Discard, req); err != nil {
		return nil, err
	}
	if c.isClosed() {
		return nil, ErrClosed
	}

	txID := uint16(c.nextTx.Add(1))
	frame, err := modbus.TypedFrame{TransactionID: txID, UnitID: unit, Message: req}.Frame()
	if err != nil {
		return nil, err
	}
	raw, err := frame.Marshal()
	if err != nil {
		return nil, err
	}

	// One request in flight: wait for the slot or for this caller's context.
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.isClosed() {
		return nil, ErrClosed
	}

	attempts := c.retries + 1
	var (
		lastOutcome outcome
		lastErr     error
	)
	for attempt := 1; attempt <= attempts; attempt++ {
		var start time.Time
		if c.metrics != nil {
			start = time.Now()
		}

		msg, oc, err := c.once(ctx, unit, txID, raw, req)
		switch oc {
		case outcomeOK:
			c.record(req.Function(), ResultOK, start)
			return msg, nil
		case outcomeException:
			// A delivered answer: no retry, and the connection stays usable.
			c.record(req.Function(), ResultException, start)
			return nil, err
		case outcomeMismatch:
			// The stream no longer answers the requests we send on it.
			c.record(req.Function(), ResultMismatch, start)
			c.kill()
			return nil, err
		case outcomeClosed:
			c.record(req.Function(), ResultTransport, start)
			return nil, ErrClosed
		case outcomeProtocol:
			// A frame the codec rejected: the peer is not answering this dialect.
			c.record(req.Function(), ResultTransport, start)
			c.kill()
			return nil, err
		case outcomeContext:
			// The caller gave up. The reply may still be in flight, so the
			// connection cannot be trusted for a later request.
			c.record(req.Function(), ResultTransport, start)
			c.kill()
			return nil, err
		case outcomeTimeout:
			c.record(req.Function(), ResultTimeout, start)
		default:
			c.record(req.Function(), ResultTransport, start)
		}

		lastOutcome, lastErr = oc, err
		// The failed attempt's connection may hold a partial or stale reply.
		c.drop()
		if c.isClosed() {
			return nil, ErrClosed
		}
	}

	// Every attempt failed. There is no live connection to fall back to, so the
	// client reports ErrClosed from now on instead of looking healthy.
	c.kill()
	if lastOutcome == outcomeTimeout {
		return nil, fmt.Errorf("%w: unit %d function %#02x after %d attempt(s): %v",
			ErrTimeout, unit, req.Function(), attempts, lastErr)
	}
	return nil, fmt.Errorf("gmodbus: unit %d function %#02x after %d attempt(s): %w",
		unit, req.Function(), attempts, lastErr)
}

// once performs one attempt on a live connection. It returns the decoded
// response, the outcome class and the error to surface if the outcome is final.
func (c *Client) once(ctx context.Context, unit uint8, txID uint16, frame []byte, req modbus.Message) (modbus.Message, outcome, error) {
	conn, r, err := c.connection(ctx)
	if err != nil {
		if errors.Is(err, ErrClosed) {
			return nil, outcomeClosed, err
		}
		return nil, c.classify(ctx, err), err
	}

	// The attempt deadline is the earlier of Timeout and the caller's deadline.
	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, outcomeTransport, err
	}
	// A caller that cancels before the deadline must not wait for it: pushing
	// the deadline into the past aborts the pending write or read.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer func() {
		if !stop() {
			// The callback already ran and may have left a deadline in the past on
			// this connection, so it must not carry the next exchange.
			c.drop()
		}
	}()

	if err := writeAll(conn, frame); err != nil {
		return nil, c.classify(ctx, err), err
	}
	typed, err := modbus.ReadTypedResponse(r, c.maxPDU, req)
	if err != nil {
		return nil, c.classify(ctx, err), err
	}
	if typed.TransactionID != txID || typed.UnitID != unit {
		return nil, outcomeMismatch, fmt.Errorf("%w: got transaction %d unit %d, want transaction %d unit %d",
			ErrTransactionMismatch, typed.TransactionID, typed.UnitID, txID, unit)
	}
	if exception, ok := typed.Message.(*modbus.Exception); ok {
		return nil, outcomeException, &Error{Unit: typed.UnitID, Function: exception.FunctionCode, Code: exception.Code}
	}
	return typed.Message, outcomeOK, nil
}

// connection returns the live connection, dialing one if the previous attempt
// dropped it.
func (c *Client) connection(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if c.conn != nil {
		conn, r := c.conn, c.r
		c.mu.Unlock()
		return conn, r, nil
	}
	c.mu.Unlock()

	// Only one exchange runs at a time, so only one goroutine dials here.
	conn, err := dialTCP(ctx, c.addr, c.dialTimeout)
	if err != nil {
		return nil, nil, err
	}
	r := bufio.NewReader(conn)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = conn.Close()
		return nil, nil, ErrClosed
	}
	c.conn, c.r = conn, r
	return conn, r, nil
}

// classify maps an exchange error to an outcome.
func (c *Client) classify(ctx context.Context, err error) outcome {
	if ctx.Err() != nil {
		return outcomeContext
	}
	var netErr net.Error
	if errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return outcomeTimeout
	}
	switch wire.KindOf(err) {
	case wire.KindMalformed, wire.KindTooLarge, wire.KindUnsupported:
		return outcomeProtocol
	default:
		// io.EOF, a reset, an incomplete frame, a refused connect: the transport
		// failed and the connection cannot be reused.
		return outcomeTransport
	}
}

// isClosed reports whether the client has no connection left.
func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// drop closes the current connection but keeps the client usable, so the next
// attempt dials again.
func (c *Client) drop() {
	c.mu.Lock()
	conn := c.conn
	c.conn, c.r = nil, nil
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// kill closes the connection and makes the client fail fast from now on.
func (c *Client) kill() {
	c.mu.Lock()
	conn := c.conn
	c.conn, c.r = nil, nil
	c.closed = true
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// dialTCP connects and keeps the address in the error, so the caller sees which
// server failed.
func dialTCP(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("gmodbus: dial %s: %w", addr, err)
	}
	return conn, nil
}

// writeAll writes the whole frame. A stream write may be short, and the
// response reader must start at the frame boundary.
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

// unexpectedResponse reports a response the codec should have shaped for the
// request. It cannot happen unless the codec and this package disagree.
func unexpectedResponse(op string, msg modbus.Message) error {
	return fmt.Errorf("gmodbus: %s: unexpected response %T", op, msg)
}
