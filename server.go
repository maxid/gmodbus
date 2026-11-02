package gmodbus

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/maxid/gnety"
	"github.com/maxid/gnety/codec/modbus"
	"github.com/pion/dtls/v2"
)

// ServerConfig configures a Server. A zero timing field takes the package
// default; a negative one is rejected by NewServer.
type ServerConfig struct {
	// Network selects the transport. The zero value is NetworkTCP.
	Network Network
	// Addr is the listen address, "host:port", for the network transports. For
	// NetworkRTU it is the serial device path; it is ignored when Port is set.
	Addr string
	// TLS configures NetworkTLS. It is required unless Listener already
	// terminates TLS.
	TLS *tls.Config
	// DTLS configures NetworkDTLS. It is required unless Listener already
	// terminates DTLS.
	DTLS *dtls.Config
	// Serial configures the line parameters of NetworkRTU. Nil means 19200 8N1.
	Serial *SerialConfig
	// Port supplies an already-open serial line for NetworkRTU. When set, Addr
	// and Serial are ignored and the caller keeps ownership until Close.
	Port Port
	// RTU selects the RTU framing variant for NetworkRTUOverTCP and
	// NetworkRTUOverUDP. NoCRC is rejected for NetworkRTU.
	RTU modbus.RTUOptions
	// Listener replaces the socket of a stream transport (tcp, tls, rtu-tcp,
	// dtls). For tls and dtls it must already be a TLS/DTLS listener. The server
	// closes it on Close.
	Listener net.Listener
	// PacketConn replaces the socket of the udp and rtu-udp transports. It is
	// ignored for the others. The server closes it on Close.
	PacketConn net.PacketConn
	// Handler answers requests before the built-in store. Nil means the store
	// answers everything it knows.
	Handler Handler
	// Store is the built-in data model. Nil creates a store with unit 1 and the
	// default bank size.
	Store *MemoryStore
	// Device is the identity reported for function code 0x2B/0x0E when the
	// handler and store leave the request unhandled. Nil rejects those requests
	// with IllegalFunction.
	Device *DeviceIdentification
	// Functions registers vendor function codes, or replaces a standard code's
	// payload. Nil means the built-in function codes only.
	Functions modbus.Functions
	// MaxPDU bounds an inbound PDU. Zero means modbus.MaxPDU; a larger value is
	// rejected.
	MaxPDU int
	// MaxConnections bounds concurrent stream connections and datagram peers.
	// Zero means unlimited.
	MaxConnections int
	// IdleTimeout closes a stream connection that sends no request within the
	// interval and forgets an idle datagram peer. It does not apply to the
	// serial transport. Zero disables it.
	IdleTimeout time.Duration
	// ShutdownTimeout bounds Close. Zero means DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
	// Metrics receives one event per served request. Nil costs nothing.
	Metrics ServerMetrics
	// OnError receives transport and handler errors. Nil discards them; the
	// callback runs on the serving goroutine and must not block.
	OnError func(error)
}

// DefaultShutdownTimeout bounds Server.Close when ShutdownTimeout is zero.
const DefaultShutdownTimeout = 5 * time.Second

// Server is one Modbus server endpoint.
//
// NewServer binds the transport and starts serving; Close stops it. The server
// is safe for concurrent use by the transport goroutines; Handler and OnError
// may be called concurrently and must be safe.
type Server struct {
	network   Network
	addr      string
	handler   Handler
	store     *MemoryStore
	device    *DeviceIdentification
	functions modbus.Functions
	rtu       modbus.RTUOptions
	maxPDU    int
	metrics   ServerMetrics
	onError   func(error)

	idleTimeout     time.Duration
	shutdownTimeout time.Duration

	// gnety carries every network transport; nil for the serial transport.
	gnety *gnety.Server
	// serial carries the RTU serial transport; nil for the others.
	serial Port

	closeCtx    context.Context
	closeCancel context.CancelFunc
	wg          sync.WaitGroup

	mu     sync.Mutex
	closed bool
}

// NewServer binds cfg and starts serving. It returns once the listener (or the
// serial port) is ready, so Addr is usable immediately.
func NewServer(ctx context.Context, cfg ServerConfig) (*Server, error) {
	cfg, err := normalizeServerConfig(cfg)
	if err != nil {
		return nil, err
	}
	closeCtx, closeCancel := context.WithCancel(context.Background())
	s := &Server{
		network:         cfg.Network,
		addr:            cfg.Addr,
		handler:         cfg.Handler,
		store:           cfg.Store,
		device:          cfg.Device,
		functions:       cfg.Functions,
		rtu:             cfg.RTU,
		maxPDU:          cfg.MaxPDU,
		metrics:         cfg.Metrics,
		onError:         cfg.OnError,
		idleTimeout:     cfg.IdleTimeout,
		shutdownTimeout: cfg.ShutdownTimeout,
		closeCtx:        closeCtx,
		closeCancel:     closeCancel,
	}
	if cfg.Network.serial() {
		port, err := s.openPort(cfg)
		if err != nil {
			closeCancel()
			return nil, err
		}
		s.serial = port
		s.wg.Add(1)
		go s.serveSerial()
	} else {
		gnetyCfg := gnety.Config{
			Network:         gnetyNetwork(cfg.Network),
			Addr:            cfg.Addr,
			Listener:        cfg.Listener,
			PacketConn:      cfg.PacketConn,
			MaxSessions:     cfg.MaxConnections,
			IdleTimeout:     cfg.IdleTimeout,
			ShutdownTimeout: cfg.ShutdownTimeout,
		}
		switch cfg.Network {
		case NetworkTCP, NetworkRTUOverTCP:
			gnetyCfg.RawHandler = s.serveStream
		case NetworkTLS:
			gnetyCfg.RawHandler = s.serveStream
			gnetyCfg.TLSConfig = cfg.TLS
		case NetworkUDP, NetworkRTUOverUDP:
			gnetyCfg.DatagramCodec = serverDatagramCodec{s}
			gnetyCfg.Handler = s.datagramHandler()
		case NetworkDTLS:
			gnetyCfg.DTLSConfig = cfg.DTLS
			gnetyCfg.DatagramCodec = serverDatagramCodec{s}
			gnetyCfg.Handler = s.datagramHandler()
		}
		gnetySrv, err := gnety.New(gnetyCfg)
		if err != nil {
			closeCancel()
			return nil, err
		}
		if err := gnetySrv.Start(ctx); err != nil {
			closeCancel()
			return nil, err
		}
		s.gnety = gnetySrv
	}
	go s.watchContext(ctx)
	return s, nil
}

// openPort returns the caller-supplied serial port or opens the configured
// device.
func (s *Server) openPort(cfg ServerConfig) (Port, error) {
	if cfg.Port != nil {
		return cfg.Port, nil
	}
	return openSerialPort(cfg.Addr, *cfg.Serial)
}

// normalizeServerConfig validates cfg and fills defaults.
func normalizeServerConfig(cfg ServerConfig) (ServerConfig, error) {
	if cfg.Network == "" {
		cfg.Network = NetworkTCP
	}
	if !cfg.Network.valid() {
		return cfg, fmt.Errorf("gmodbus: unknown Network %q", cfg.Network)
	}
	if cfg.Network.serial() {
		if cfg.Port == nil && cfg.Addr == "" {
			return cfg, errors.New("gmodbus: ServerConfig.Addr is required for the RTU transport")
		}
		if cfg.Serial != nil && cfg.Port != nil {
			return cfg, errors.New("gmodbus: ServerConfig.Serial is unused when ServerConfig.Port is set")
		}
		if cfg.RTU.NoCRC {
			return cfg, errors.New("gmodbus: ServerConfig.RTU.NoCRC is not allowed on the serial RTU transport")
		}
	} else {
		if cfg.Addr == "" && cfg.Listener == nil && cfg.PacketConn == nil {
			return cfg, errors.New("gmodbus: ServerConfig.Addr (or Listener/PacketConn) is required")
		}
		if cfg.Port != nil {
			return cfg, errors.New("gmodbus: ServerConfig.Port is only valid for the RTU transport")
		}
		if cfg.Serial != nil {
			return cfg, errors.New("gmodbus: ServerConfig.Serial is only valid for the RTU transport")
		}
	}
	if cfg.PacketConn != nil && cfg.Network != NetworkUDP && cfg.Network != NetworkRTUOverUDP {
		return cfg, errors.New("gmodbus: ServerConfig.PacketConn is only valid for the UDP transports")
	}
	if cfg.Listener != nil && (cfg.Network == NetworkUDP || cfg.Network == NetworkRTUOverUDP) {
		return cfg, errors.New("gmodbus: ServerConfig.Listener is not valid for the UDP transports")
	}
	if cfg.Listener != nil && cfg.Network == NetworkUDP {
		return cfg, errors.New("gmodbus: ServerConfig.Listener is not valid for the UDP transport")
	}
	if cfg.TLS != nil && cfg.Network != NetworkTLS {
		return cfg, errors.New("gmodbus: ServerConfig.TLS is only valid for the TLS transport")
	}
	if cfg.DTLS != nil && cfg.Network != NetworkDTLS {
		return cfg, errors.New("gmodbus: ServerConfig.DTLS is only valid for the DTLS transport")
	}
	if cfg.Network == NetworkTLS && cfg.TLS == nil && cfg.Listener == nil {
		return cfg, errors.New("gmodbus: ServerConfig.TLS is required for the TLS transport")
	}
	if cfg.Network == NetworkDTLS && cfg.DTLS == nil && cfg.Listener == nil {
		return cfg, errors.New("gmodbus: ServerConfig.DTLS is required for the DTLS transport")
	}
	if cfg.MaxPDU < 0 || cfg.MaxPDU > modbus.MaxPDU {
		return cfg, fmt.Errorf("gmodbus: ServerConfig.MaxPDU %d is outside 0..%d", cfg.MaxPDU, modbus.MaxPDU)
	}
	if cfg.MaxConnections < 0 {
		return cfg, errors.New("gmodbus: ServerConfig.MaxConnections is negative")
	}
	if cfg.IdleTimeout < 0 {
		return cfg, errors.New("gmodbus: ServerConfig.IdleTimeout is negative")
	}
	if cfg.ShutdownTimeout < 0 {
		return cfg, errors.New("gmodbus: ServerConfig.ShutdownTimeout is negative")
	}
	if cfg.MaxPDU == 0 {
		cfg.MaxPDU = modbus.MaxPDU
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	var err error
	if cfg.Serial, err = normalizeSerial(cfg.Serial); err != nil {
		return cfg, err
	}
	if cfg.Store == nil {
		cfg.Store = NewMemoryStore(nil)
	}
	return cfg, nil
}

// gnetyNetwork maps a public network to the gnety transport name.
func gnetyNetwork(network Network) string {
	switch network {
	case NetworkTLS:
		return "tcp-tls"
	case NetworkUDP, NetworkRTUOverUDP:
		return "udp"
	case NetworkDTLS:
		return "dtls"
	default:
		return "tcp"
	}
}

// Addr returns the actual listen address, or the serial device path.
func (s *Server) Addr() string {
	if s.gnety != nil {
		return s.gnety.Addr()
	}
	return s.addr
}

// Close stops listening, closes every open connection and closes the serial
// port. It is idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.closeCancel()
	gnetySrv := s.gnety
	serial := s.serial
	s.mu.Unlock()

	var err error
	if gnetySrv != nil {
		err = gnetySrv.Stop(context.Background())
	}
	if serial != nil {
		if closeErr := serial.Close(); err == nil {
			err = closeErr
		}
	}
	// The serial loop is the one goroutine the caller's Close cannot force: it
	// may sit in a driver that ignores a close. Bound the wait like gnety does.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(s.shutdownTimeout):
		return fmt.Errorf("gmodbus: server close did not finish within %s: %w", s.shutdownTimeout, context.DeadlineExceeded)
	}
	return err
}

// watchContext stops the server when the caller's context ends.
func (s *Server) watchContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = s.Close()
	case <-s.closeCtx.Done():
	}
}

// reportError hands one transport or handler error to OnError.
func (s *Server) reportError(err error) {
	if err == nil || s.onError == nil {
		return
	}
	s.onError(err)
}

// isClosed reports whether Close ran.
func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// dispatch resolves one request through the handler, the memory store and the
// device identification fallback, in that order. It returns the response and
// whether one must be sent at all (a broadcast write has no response).
func (s *Server) dispatch(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, bool, string) {
	if s.handler != nil {
		resp, err := s.handler.Handle(ctx, unit, req)
		if !errors.Is(err, ErrUnhandled) {
			return s.finish(req, resp, err)
		}
	}
	resp, err := s.store.Handle(ctx, unit, req)
	if !errors.Is(err, ErrUnhandled) {
		return s.finish(req, resp, err)
	}
	if idReq, ok := req.(*modbus.ReadDeviceIdentificationRequest); ok && s.device != nil {
		resp, err := s.device.handle(idReq, s.maxPDU)
		return s.finish(req, resp, err)
	}
	return s.finish(req, nil, &modbus.Exception{
		FunctionCode: req.Function(),
		Code:         modbus.ExceptionIllegalFunction,
	})
}

// finish turns a handler result into a response message. A nil response with no
// error means "send nothing".
func (s *Server) finish(req modbus.Message, resp modbus.Message, err error) (modbus.Message, bool, string) {
	if err != nil {
		var exception *modbus.Exception
		switch {
		case errors.As(err, &exception):
			return exception, true, ServerResultException
		case errors.Is(err, ErrOutOfRange):
			return exceptionFor(req, modbus.ExceptionIllegalDataAddress), true, ServerResultException
		default:
			s.reportError(err)
			return exceptionFor(req, modbus.ExceptionServerDeviceFailure), true, ServerResultError
		}
	}
	if resp == nil {
		return nil, false, ServerResultOK
	}
	if function := resp.Function(); function != req.Function() && !modbus.IsException(function) {
		s.reportError(errors.New("gmodbus: handler returned a response for another function code"))
		return exceptionFor(req, modbus.ExceptionServerDeviceFailure), true, ServerResultError
	}
	return resp, true, ServerResultOK
}

// exceptionFor builds an exception response for the request's function code.
func exceptionFor(req modbus.Message, code modbus.ExceptionCode) *modbus.Exception {
	return &modbus.Exception{FunctionCode: req.Function(), Code: code}
}

// handleRequest dispatches one decoded request, applies the RTU broadcast rule
// and reports the attempt through ServerMetrics.
func (s *Server) handleRequest(unit uint8, req modbus.Message) (resp modbus.Message, respond bool) {
	start := time.Now()
	result := ServerResultOK
	// A misbehaving handler must not take the serving goroutine with it, and the
	// attempt is reported exactly once whichever way it ends.
	defer func() {
		if recovered := recover(); recovered != nil {
			s.reportError(fmt.Errorf("gmodbus: handler panic: %v", recovered))
			resp = exceptionFor(req, modbus.ExceptionServerDeviceFailure)
			respond = true
			result = ServerResultError
		}
		if s.metrics != nil {
			s.metrics.Request(req.Function(), result, time.Since(start))
		}
	}()
	// The handler's context ends when the request is done or the server closes,
	// so a blocking handler cannot hold Close open forever.
	ctx, cancel := context.WithCancel(s.closeCtx)
	defer cancel()
	resp, respond, result = s.dispatch(ctx, unit, req)
	if s.isBroadcast(unit) {
		// A broadcast is applied but never answered.
		respond = false
	}
	return resp, respond
}
