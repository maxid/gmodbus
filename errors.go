package gmodbus

import (
	"errors"
	"fmt"

	"github.com/maxid/gnety/codec/modbus"
)

// Sentinels returned by the client and the server. They are wrapped in a longer
// message, so callers test them with errors.Is.
var (
	// ErrClosed is returned when the client has no connection: Close was called,
	// or an exchange ran out of attempts and dropped the connection. With a
	// Reconnect policy it is the result when the policy gives up, not merely when
	// one attempt failed.
	ErrClosed = errors.New("gmodbus: client is closed")
	// ErrTimeout is returned when every attempt timed out without a response.
	ErrTimeout = errors.New("gmodbus: request timed out")
	// ErrResponseMismatch is returned when a response does not answer the
	// request: a different transaction id or unit id for MBAP, a different unit
	// id or function code for RTU, or a datagram carrying another exchange's
	// reply.
	ErrResponseMismatch = errors.New("gmodbus: response does not match the request")
	// ErrTLSConfig is wrapped by a TLS or DTLS handshake failure that a retry
	// cannot fix: a certificate that does not verify (unknown authority,
	// expired, wrong host name) or a peer that does not speak the protocol. A
	// reconnect gives up on it instead of spending its attempt budget.
	ErrTLSConfig = errors.New("gmodbus: TLS configuration error")
	// ErrUnhandled is returned by a Handler that does not want a request. The
	// server then asks the default MemoryStore instead of sending an exception.
	ErrUnhandled = errors.New("gmodbus: request not handled")
	// ErrOutOfRange is returned by MemoryStore and UnitStore methods when an
	// address or quantity falls outside the configured bank.
	ErrOutOfRange = errors.New("gmodbus: address out of range")
)

// Error is a Modbus exception response mapped to an error.
//
// A server that rejects a request answers with the request function code, the
// exception bit set, and one exception code. The reply is a delivered answer:
// the client never retries it and keeps the connection.
//
// Unwrap reports modbus.ErrException, so errors.Is(err, modbus.ErrException)
// matches every code. The typed value is recovered with errors.As:
//
//	var me *gmodbus.Error
//	if errors.As(err, &me) && me.Code == modbus.ExceptionIllegalDataAddress {
//		// the address is not implemented on the device
//	}
type Error struct {
	// Unit is the unit id the request was addressed to.
	Unit uint8
	// Function is the function code of the rejected request, exception bit clear.
	Function byte
	// Code is the exception code the server reported.
	Code modbus.ExceptionCode
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("gmodbus: unit %d function %#02x: %s", e.Unit, e.Function, e.Code)
}

// Unwrap reports modbus.ErrException.
func (e *Error) Unwrap() error { return modbus.ErrException }
