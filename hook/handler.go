// Package hook is the extension contract of this package: the interfaces a consumer
// implements or supplies to change how a client or a server behaves.
//
// It exists as its own package so the extension surface is one visible directory instead of
// sections of two implementation files. Nothing here imports the root package, so an
// implementation cannot reach internals it is not supposed to see, and the root package
// re-exports every name as an alias — existing code keeps using gmodbus.Handler.
package hook

import (
	"context"
	"errors"

	"github.com/maxid/gnety/codec/modbus"
)

// ErrUnhandled is returned by a Handler that does not want a request. The server then asks
// the default MemoryStore instead of sending an exception.
var ErrUnhandled = errors.New("gmodbus: request not handled")

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
