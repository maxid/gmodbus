package gmodbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/maxid/gnety/codec/modbus"
)

// Handler answers server requests. It runs before the built-in MemoryStore, so
// it can take over a standard function code, a vendor function code registered
// in ServerConfig.Functions, or one unit id only.
//
// Returning a response message sends it back; returning a *modbus.Exception
// (as an error or as a message) sends that exception. Returning ErrUnhandled
// asks the server to fall back to the MemoryStore. Any other error is reported
// through ServerConfig.OnError and becomes a ServerDeviceFailure exception; a
// panic is recovered and reported the same way.
//
// ctx is cancelled when the request finishes or the server closes, so a handler
// that waits on it releases Server.Close. The handler must not retain ctx.
type Handler interface {
	Handle(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error)

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
	return f(ctx, unit, req)
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
