package gmodbus

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maxid/gnety"
	"github.com/maxid/gnety/codec/modbus"
	"github.com/maxid/gnety/codec/wire"
	"github.com/pion/dtls/v2"
)

// Defaults used when a ClientConfig or ServerConfig field is zero.
const (
	// DefaultTimeout bounds one attempt: the write and the matching read share
	// it.
	DefaultTimeout = 5 * time.Second
	// DefaultDialTimeout bounds one connect, including the TLS/DTLS handshake
	// and a serial line open.
	DefaultDialTimeout = 5 * time.Second
)

// ClientConfig configures a Client. A zero timing field takes the package
// default; a negative one is rejected by NewClient.
type ClientConfig struct {
	// Network selects the transport. The zero value is NetworkTCP.
	Network Network
	// Addr is the server address, "host:port", for the network transports. For
	// NetworkRTU it is the serial device path, for example "/dev/ttyUSB0" or
	// "COM3"; it is ignored when Port is set.
	Addr string
	// DialTimeout bounds one connect and the TLS/DTLS handshake. Zero means
	// DefaultDialTimeout.
	DialTimeout time.Duration
	// Timeout bounds one attempt. Zero means DefaultTimeout.
	Timeout time.Duration
	// Retries is how many extra attempts follow the first when an attempt times
	// out or hits a transport failure. Zero means a single attempt. With a
	// Reconnect policy, transport failures use the policy's attempt budget and
	// do not consume Retries.
	Retries int
	// MaxPDU bounds an inbound PDU. Zero means modbus.MaxPDU; a larger value is
	// rejected.
	MaxPDU int
	// Metrics receives one event per attempt. Nil costs nothing.
	Metrics Metrics
	// Reconnect enables automatic recovery from a transport failure or a
	// connection closed by the peer. Nil keeps the fail-fast behavior.
	Reconnect *ReconnectPolicy
	// TLS configures NetworkTLS. When nil a default configuration that trusts
	// the system roots is used, with ServerName taken from Addr.
	TLS *tls.Config
	// DTLS configures NetworkDTLS. It is required for that transport.
	DTLS *dtls.Config
	// Serial configures the line parameters of NetworkRTU. Nil means 19200 8N1.
	Serial *SerialConfig
	// RTU selects the RTU framing variant for NetworkRTUOverTCP and
	// NetworkRTUOverUDP. NoCRC is rejected for NetworkRTU: a serial line must
	// carry the checksum.
	RTU modbus.RTUOptions
	// Functions registers vendor function codes, or replaces a standard code's
	// payload. Nil means the built-in function codes only.
	Functions modbus.Functions
	// Port supplies an already-open serial line for NetworkRTU. When set, Addr
	// and Serial are ignored and the caller keeps ownership until Close.
	Port Port
}

// Client is one Modbus client connection.
//
// A Client is safe for concurrent use; its operations are serialized on the
// wire. Close makes the client unusable. Without a reconnect policy a client
// whose exchange ran out of attempts is closed too; with one it stays usable
// after a timeout and recovers from transport failures within the policy.
type Client struct {
	network     Network
	addr        string
	timeout     time.Duration
	dialTimeout time.Duration
	retries     int
	maxPDU      int
	metrics     Metrics

	reconnectPolicy *ReconnectPolicy
	tlsConfig       *tls.Config
	dtlsConfig      *dtls.Config
	serial          SerialConfig
	port            Port
	rtu             modbus.RTUOptions
	functions       modbus.Functions
	jitter          func(int64) int64

	// closeCtx ends every wait and cancels every in-flight connect when Close is
	// called.
	closeCtx    context.Context
	closeCancel context.CancelFunc

	// nextTx hands out transaction ids for MBAP transports. Wraparound is
	// tolerated: a reused id only matters for a reply that is still in flight, and
	// the client never leaves one behind on a live connection.
	nextTx atomic.Uint32

	// sem holds the single exchange slot. Sending into it acquires the slot,
	// receiving from it releases it, and a caller that cannot acquire it waits on
	// its own context.
	sem chan struct{}

	// mu guards the connection state. It is never held across network I/O.
	mu     sync.Mutex
	link   *clientLink
	closed bool
}

// NewClient opens one connection for cfg and returns a client for it.
//
// NewClient does no request: the connection is verified by the first operation.
// A TLS/DTLS handshake counts as part of the connect and is bounded by
// DialTimeout.
func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	cfg, err := normalizeClientConfig(cfg)
	if err != nil {
		return nil, err
	}

	closeCtx, closeCancel := context.WithCancel(context.Background())
	c := &Client{
		network:         cfg.Network,
		addr:            cfg.Addr,
		timeout:         cfg.Timeout,
		dialTimeout:     cfg.DialTimeout,
		retries:         cfg.Retries,
		maxPDU:          cfg.MaxPDU,
		metrics:         cfg.Metrics,
		reconnectPolicy: cfg.Reconnect,
		tlsConfig:       cfg.TLS,
		dtlsConfig:      cfg.DTLS,
		serial:          *cfg.Serial,
		port:            cfg.Port,
		rtu:             cfg.RTU,
		functions:       cfg.Functions,
		jitter:          jitterInt64N,
		closeCtx:        closeCtx,
		closeCancel:     closeCancel,
		sem:             make(chan struct{}, 1),
	}
	link, err := c.open(ctx)
	if err != nil {
		closeCancel()
		return nil, err
	}
	if err := c.install(link); err != nil {
		closeCancel()
		return nil, err
	}
	return c, nil
}

// normalizeClientConfig validates cfg, fills defaults and clones the security
// configuration so a caller cannot race the client.
func normalizeClientConfig(cfg ClientConfig) (ClientConfig, error) {
	if cfg.Network == "" {
		cfg.Network = NetworkTCP
	}
	if !cfg.Network.valid() {
		return cfg, fmt.Errorf("gmodbus: unknown Network %q", cfg.Network)
	}
	if cfg.Network.serial() {
		if cfg.Port == nil && cfg.Addr == "" {
			return cfg, errors.New("gmodbus: Config.Addr is required for the RTU transport")
		}
		if cfg.Serial != nil && cfg.Port != nil {
			return cfg, errors.New("gmodbus: Config.Serial is unused when Config.Port is set")
		}
		if cfg.RTU.NoCRC {
			return cfg, errors.New("gmodbus: Config.RTU.NoCRC is not allowed on the serial RTU transport")
		}
	} else {
		if cfg.Addr == "" {
			return cfg, errors.New("gmodbus: Config.Addr is required")
		}
		if cfg.Port != nil {
			return cfg, errors.New("gmodbus: Config.Port is only valid for the RTU transport")
		}
		if cfg.Serial != nil {
			return cfg, errors.New("gmodbus: Config.Serial is only valid for the RTU transport")
		}
	}
	if cfg.TLS != nil && cfg.Network != NetworkTLS {
		return cfg, errors.New("gmodbus: Config.TLS is only valid for the TLS transport")
	}
	if cfg.DTLS != nil && cfg.Network != NetworkDTLS {
		return cfg, errors.New("gmodbus: Config.DTLS is only valid for the DTLS transport")
	}
	if cfg.Network == NetworkDTLS && cfg.DTLS == nil {
		return cfg, errors.New("gmodbus: Config.DTLS is required for the DTLS transport")
	}
	if cfg.DialTimeout < 0 {
		return cfg, errors.New("gmodbus: Config.DialTimeout is negative")
	}
	if cfg.Timeout < 0 {
		return cfg, errors.New("gmodbus: Config.Timeout is negative")
	}
	if cfg.Retries < 0 {
		return cfg, errors.New("gmodbus: Config.Retries is negative")
	}
	if cfg.MaxPDU < 0 || cfg.MaxPDU > modbus.MaxPDU {
		return cfg, fmt.Errorf("gmodbus: Config.MaxPDU %d is outside 0..%d", cfg.MaxPDU, modbus.MaxPDU)
	}
	reconnect, err := normalizeReconnect(cfg.Reconnect)
	if err != nil {
		return cfg, err
	}
	cfg.Reconnect = reconnect
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxPDU == 0 {
		cfg.MaxPDU = modbus.MaxPDU
	}
	if cfg.Serial, err = normalizeSerial(cfg.Serial); err != nil {
		return cfg, err
	}

	switch cfg.Network {
	case NetworkTLS:
		if cfg.TLS == nil {
			cfg.TLS = &tls.Config{}
		} else {
			cfg.TLS = cfg.TLS.Clone()
		}
		if cfg.TLS.ServerName == "" {
			if host, _, splitErr := net.SplitHostPort(cfg.Addr); splitErr == nil {
				cfg.TLS.ServerName = host
			}
		}
	case NetworkDTLS:
		dtlsCfg := *cfg.DTLS
		if dtlsCfg.ServerName == "" {
			if host, _, splitErr := net.SplitHostPort(cfg.Addr); splitErr == nil {
				dtlsCfg.ServerName = host
			}
		}
		if dtlsCfg.ConnectContextMaker == nil {
			timeout := cfg.DialTimeout
			dtlsCfg.ConnectContextMaker = func() (context.Context, func()) {
				return context.WithTimeout(context.Background(), timeout)
			}
		}
		cfg.DTLS = &dtlsCfg
	}
	return cfg, nil
}

// Addr returns the address the client was opened with.
func (c *Client) Addr() string { return c.addr }

// Close closes the connection and stops any reconnect wait or connect. It is
// idempotent: a second Close reports nil and leaves a client that is already
// closed untouched.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.closeCancel()
	link := c.link
	c.link = nil
	c.mu.Unlock()
	if link == nil {
		return nil
	}
	return link.close()
}

// ReadCoils reads quantity coils starting at address (function code 0x01).
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

// ReadDiscreteInputs reads quantity discrete inputs (function code 0x02).
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

// ReadHoldingRegisters reads quantity holding registers (function code 0x03).
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

// ReadInputRegisters reads quantity input registers (function code 0x04).
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

// WriteSingleCoil writes one coil (function code 0x05). The server must echo
// the address and value, otherwise the codec rejects the response.
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

// WriteMultipleCoils writes len(values) coils (function code 0x0F). The server
// must acknowledge the same address and quantity.
func (c *Client) WriteMultipleCoils(ctx context.Context, unit uint8, address uint16, values []bool) error {
	_, err := c.do(ctx, unit, &modbus.WriteMultipleCoilsRequest{Address: address, Values: values})
	return err
}

// WriteMultipleRegisters writes len(values) holding registers (function code
// 0x10). The server must acknowledge the same address and quantity.
func (c *Client) WriteMultipleRegisters(ctx context.Context, unit uint8, address uint16, values []uint16) error {
	_, err := c.do(ctx, unit, &modbus.WriteMultipleRegistersRequest{Address: address, Values: values})
	return err
}

// MaskWriteRegister applies andMask and orMask to one holding register
// (function code 0x16): the new value is (current AND andMask) OR (orMask AND
// NOT andMask).
func (c *Client) MaskWriteRegister(ctx context.Context, unit uint8, address, andMask, orMask uint16) error {
	_, err := c.do(ctx, unit, modbus.MaskWriteRegister{Address: address, AndMask: andMask, OrMask: orMask})
	return err
}

// ReadWriteMultipleRegisters writes values at writeAddress and then reads
// readQuantity registers from readAddress in one transaction (function code
// 0x17).
func (c *Client) ReadWriteMultipleRegisters(ctx context.Context, unit uint8, readAddress, readQuantity, writeAddress uint16, values []uint16) ([]uint16, error) {
	msg, err := c.do(ctx, unit, &modbus.ReadWriteMultipleRegistersRequest{
		ReadAddress:  readAddress,
		ReadQuantity: readQuantity,
		WriteAddress: writeAddress,
		WriteValues:  values,
	})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*modbus.ReadWriteMultipleRegistersResponse)
	if !ok {
		return nil, unexpectedResponse("read/write multiple registers", msg)
	}
	return resp.Registers, nil
}

// ReadDeviceIdentification reads one device identification response (function
// code 0x2B/0x0E). readCode selects the object group (modbus.DeviceIDBasic and
// friends); objectID selects the first object for modbus.DeviceIDIndividual.
//
// The response may set MoreFollows and NextObjectID; the caller continues with
// another request carrying NextObjectID.
func (c *Client) ReadDeviceIdentification(ctx context.Context, unit uint8, readCode, objectID byte) (*modbus.ReadDeviceIdentificationResponse, error) {
	msg, err := c.do(ctx, unit, &modbus.ReadDeviceIdentificationRequest{ReadCode: readCode, ObjectID: objectID})
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*modbus.ReadDeviceIdentificationResponse)
	if !ok {
		return nil, unexpectedResponse("read device identification", msg)
	}
	return resp, nil
}

// Execute sends req and returns the decoded response. It is the typed entry
// point for a vendor function code registered in ClientConfig.Functions and for
// any standard request type the typed methods do not cover.
func (c *Client) Execute(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
	return c.do(ctx, unit, req)
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

// do runs one transaction: validation, frame building, the serialized attempt
// loop and the mapping of the final result.
func (c *Client) do(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Validate the request through the codec before taking the exchange slot: a
	// bad address, quantity or value list costs no attempt and no round trip.
	var pdu bytes.Buffer
	if err := modbus.EncodePDU(&pdu, req); err != nil {
		return nil, err
	}
	// On the RTU transports unit 0 is a broadcast: a write is sent once and
	// never answered, and a read has no meaning.
	broadcast := c.network.rtuFamily() && unit == 0
	if broadcast && !broadcastable(req) {
		return nil, errors.New("gmodbus: unit 0 is a broadcast for the single and multiple write function codes")
	}
	if c.isClosed() {
		return nil, ErrClosed
	}

	txID := uint16(c.nextTx.Add(1))
	raw := pdu.Bytes()

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

	maxAttempts := c.retries + 1
	policy := c.reconnectPolicy
	reconnectBudget := 0
	if policy != nil {
		reconnectBudget = policy.MaxAttempts
	}

	var lastErr error
	for attempt := 1; ; attempt++ {
		var start time.Time
		if c.metrics != nil {
			start = time.Now()
		}

		msg, oc, err := c.once(ctx, unit, txID, req, raw, broadcast)
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

		lastErr = err
		// The failed attempt's connection may hold a partial or stale reply.
		c.drop()
		if c.isClosed() {
			return nil, ErrClosed
		}

		if oc == outcomeTimeout {
			if attempt >= maxAttempts {
				if policy == nil {
					c.kill()
				}
				return nil, fmt.Errorf("%w: unit %d function %#02x after %d attempt(s): %v",
					ErrTimeout, unit, req.Function(), attempt, lastErr)
			}
			continue
		}

		// A transport failure without a reconnect policy follows the old retry
		// budget; the dial on the next attempt is the whole recovery.
		if policy == nil {
			if attempt >= maxAttempts {
				c.kill()
				return nil, fmt.Errorf("gmodbus: unit %d function %#02x after %d attempt(s): %w",
					unit, req.Function(), attempt, lastErr)
			}
			continue
		}

		// Reconnection is enabled. A bad TLS configuration cannot be fixed by a
		// fresh connection, so do not spend the remaining budget on it.
		if isTLSConfigError(lastErr) {
			c.kill()
			return nil, fmt.Errorf("%w: unit %d function %#02x: %w",
				ErrClosed, unit, req.Function(), lastErr)
		}
		if reconnectBudget == 0 {
			c.kill()
			return nil, fmt.Errorf("%w: unit %d function %#02x after %d attempt(s): %w",
				ErrClosed, unit, req.Function(), attempt, lastErr)
		}
		if err := c.reconnect(ctx, &reconnectBudget, lastErr); err != nil {
			c.kill()
			switch {
			case errors.Is(err, ErrClosed):
				return nil, ErrClosed
			case ctx.Err() != nil:
				return nil, ctx.Err()
			default:
				return nil, fmt.Errorf("%w: unit %d function %#02x after %d attempt(s): %w",
					ErrClosed, unit, req.Function(), attempt, err)
			}
		}
		// A live connection is back: retry the in-flight request with the same
		// transaction id.
	}
}

// once performs one attempt on a live connection. It returns the decoded
// response, the outcome class and the error to surface if the outcome is final.
func (c *Client) once(ctx context.Context, unit uint8, txID uint16, req modbus.Message, pdu []byte, broadcast bool) (modbus.Message, outcome, error) {
	link, err := c.connection(ctx)
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
	attemptCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := link.setDeadline(deadline); err != nil {
		if errors.Is(err, gnety.ErrSessionClosed) {
			return nil, outcomeClosed, ErrClosed
		}
		return nil, c.classify(ctx, err), err
	}
	// A caller that cancels before the deadline must not wait for it: pushing
	// the deadline into the past aborts the pending write or read.
	stop := context.AfterFunc(ctx, func() { _ = link.setDeadline(time.Unix(1, 0)) })
	defer func() {
		if !stop() {
			// The callback already ran and may have left a deadline in the past on
			// this connection, so it must not carry the next exchange.
			c.drop()
		}
	}()

	link.codec.setPending(unit, txID, req)
	defer link.codec.clearPending()

	if err := link.send(clientFrame{unit: unit, txID: txID, pdu: pdu}); err != nil {
		return nil, c.classify(ctx, err), err
	}
	if broadcast {
		// A broadcast is a write with no reply; the exchange ends with the write.
		return nil, outcomeOK, nil
	}
	for {
		msg, err := link.receive(attemptCtx)
		if err != nil {
			// A datagram can carry a late reply to an earlier exchange; it is not
			// this request's answer, so keep waiting for one that is.
			if c.network.datagram() && errors.Is(err, ErrResponseMismatch) {
				continue
			}
			return nil, c.classify(ctx, err), err
		}
		if exception, ok := msg.(*modbus.Exception); ok {
			return nil, outcomeException, &Error{Unit: unit, Function: exception.FunctionCode, Code: exception.Code}
		}
		return msg, outcomeOK, nil
	}
}

// connection returns the live link, opening one if the previous attempt
// dropped it.
func (c *Client) connection(ctx context.Context) (*clientLink, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if c.link != nil {
		link := c.link
		c.mu.Unlock()
		return link, nil
	}
	c.mu.Unlock()

	// Only one exchange runs at a time, so only one goroutine opens a link here.
	link, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.install(link); err != nil {
		return nil, err
	}
	return link, nil
}

// install makes link the live connection unless the client was closed while the
// connect was in flight.
func (c *Client) install(link *clientLink) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = link.close()
		return ErrClosed
	}
	c.link = link
	return nil
}

// classify maps an exchange error to an outcome.
func (c *Client) classify(ctx context.Context, err error) outcome {
	if ctx.Err() != nil {
		return outcomeContext
	}
	if errors.Is(err, ErrResponseMismatch) {
		return outcomeMismatch
	}
	var netErr interface{ Timeout() bool }
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
// attempt connects again.
func (c *Client) drop() {
	c.mu.Lock()
	link := c.link
	c.link = nil
	c.mu.Unlock()
	if link != nil {
		_ = link.close()
	}
}

// kill closes the connection and makes the client fail fast from now on.
func (c *Client) kill() {
	c.mu.Lock()
	link := c.link
	c.link = nil
	c.closed = true
	c.closeCancel()
	c.mu.Unlock()
	if link != nil {
		_ = link.close()
	}
}

// unexpectedResponse reports a response the codec should have shaped for the
// request. It cannot happen unless the codec and this package disagree.
func unexpectedResponse(op string, msg modbus.Message) error {
	return fmt.Errorf("gmodbus: %s: unexpected response %T", op, msg)
}

// clientLink pairs the gnety client that carries the bytes with the codec that
// shapes one exchange.
type clientLink struct {
	cli   *gnety.Client
	codec clientCodec
}

func (l *clientLink) send(frame clientFrame) error { return l.cli.Send(frame) }

func (l *clientLink) receive(ctx context.Context) (modbus.Message, error) {
	value, err := l.cli.Receive(ctx)
	if err != nil {
		return nil, err
	}
	msg, ok := value.(modbus.Message)
	if !ok {
		return nil, fmt.Errorf("gmodbus: codec produced %T, not a modbus message", value)
	}
	return msg, nil
}

func (l *clientLink) setDeadline(t time.Time) error { return l.cli.SetDeadline(t) }

func (l *clientLink) close() error { return l.cli.Close() }

// clientFrame is what the client hands to the codec for encoding: one unit id,
// a transaction id used by MBAP only, and the PDU bytes.
type clientFrame struct {
	unit uint8
	txID uint16
	pdu  []byte
}

// clientCodec records the request a response must answer and turns frames into
// typed messages.
type clientCodec interface {
	setPending(unit uint8, txID uint16, req modbus.Message)
	clearPending()
}

// streamCodec implements gnety.Codec for the stream transports: MBAP or RTU
// framing over one connection.
type streamCodec struct {
	rtuMode   bool
	rtu       modbus.RTUOptions
	functions modbus.Functions
	maxPDU    int

	unit    uint8
	txID    uint16
	req     modbus.Message
	pending bool
}

func (c *streamCodec) setPending(unit uint8, txID uint16, req modbus.Message) {
	c.unit, c.txID, c.req, c.pending = unit, txID, req, true
}

func (c *streamCodec) clearPending() { c.req, c.pending = nil, false }

func (c *streamCodec) Encode(w io.Writer, value any) error {
	frame, ok := value.(clientFrame)
	if !ok {
		return fmt.Errorf("gmodbus: stream codec expects a clientFrame, got %T", value)
	}
	raw, err := c.encode(frame)
	if err != nil {
		return err
	}
	n, err := w.Write(raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	return nil
}

func (c *streamCodec) encode(frame clientFrame) ([]byte, error) {
	if c.rtuMode {
		return c.rtu.Marshal(modbus.RTUFrame{UnitID: frame.unit, PDU: frame.pdu})
	}
	return modbus.Frame{TransactionID: frame.txID, UnitID: frame.unit, PDU: frame.pdu}.Marshal()
}

func (c *streamCodec) Decode(r *bufio.Reader) (any, error) {
	if !c.pending {
		return nil, errors.New("gmodbus: response without a pending request")
	}
	if c.rtuMode {
		frame, err := c.rtu.ReadResponse(r, c.maxPDU, c.functions, c.req)
		if err != nil {
			return nil, err
		}
		return c.decode(frame.UnitID, 0, frame.PDU)
	}
	frame, err := modbus.Read(r, c.maxPDU)
	if err != nil {
		return nil, err
	}
	return c.decode(frame.UnitID, frame.TransactionID, frame.PDU)
}

func (c *streamCodec) decode(unit uint8, txID uint16, pdu []byte) (modbus.Message, error) {
	if unit != c.unit || (!c.rtuMode && txID != c.txID) {
		return nil, fmt.Errorf("%w: got transaction %d unit %d, want transaction %d unit %d",
			ErrResponseMismatch, txID, unit, c.txID, c.unit)
	}
	if err := checkFunction(c.req, pdu); err != nil {
		return nil, err
	}
	return c.functions.DecodeResponse(c.req, pdu)
}

// datagramCodec implements gnety.DatagramCodec for UDP, DTLS and RTU over UDP.
type datagramCodec struct {
	rtuMode   bool
	rtu       modbus.RTUOptions
	functions modbus.Functions
	maxPDU    int

	unit    uint8
	txID    uint16
	req     modbus.Message
	pending bool
}

func (c *datagramCodec) setPending(unit uint8, txID uint16, req modbus.Message) {
	c.unit, c.txID, c.req, c.pending = unit, txID, req, true
}

func (c *datagramCodec) clearPending() { c.req, c.pending = nil, false }

func (c *datagramCodec) Encode(value any) ([]byte, error) {
	frame, ok := value.(clientFrame)
	if !ok {
		return nil, fmt.Errorf("gmodbus: datagram codec expects a clientFrame, got %T", value)
	}
	if c.rtuMode {
		return c.rtu.Marshal(modbus.RTUFrame{UnitID: frame.unit, PDU: frame.pdu})
	}
	return modbus.Frame{TransactionID: frame.txID, UnitID: frame.unit, PDU: frame.pdu}.Marshal()
}

func (c *datagramCodec) Decode(data []byte) (any, error) {
	if !c.pending {
		return nil, errors.New("gmodbus: response without a pending request")
	}
	if c.rtuMode {
		frame, err := c.rtu.Parse(data, c.maxPDU)
		if err != nil {
			return nil, err
		}
		return c.decode(frame.UnitID, 0, frame.PDU)
	}
	frame, err := modbus.Parse(data, c.maxPDU)
	if err != nil {
		return nil, err
	}
	return c.decode(frame.UnitID, frame.TransactionID, frame.PDU)
}

func (c *datagramCodec) decode(unit uint8, txID uint16, pdu []byte) (modbus.Message, error) {
	if unit != c.unit || (!c.rtuMode && txID != c.txID) {
		return nil, fmt.Errorf("%w: got transaction %d unit %d, want transaction %d unit %d",
			ErrResponseMismatch, txID, unit, c.txID, c.unit)
	}
	if err := checkFunction(c.req, pdu); err != nil {
		return nil, err
	}
	return c.functions.DecodeResponse(c.req, pdu)
}

// checkFunction rejects a reply whose function code is neither the request's nor
// its exception form, before a decoder can misread it.
func checkFunction(req modbus.Message, pdu []byte) error {
	if len(pdu) == 0 {
		return wire.Errorf(wire.KindMalformed, "gmodbus: empty response PDU")
	}
	function := pdu[0]
	want := req.Function()
	if function != want && function != want|0x80 {
		return fmt.Errorf("%w: response function %#02x answers a %#02x request", ErrResponseMismatch, function, want)
	}
	return nil
}

// record reports one attempt when a hook is configured.
func (c *Client) record(function byte, result string, start time.Time) {
	if c.metrics == nil {
		return
	}
	c.metrics.Request(function, result, time.Since(start))
}
