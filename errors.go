package gmodbus

import (
	"errors"
	"fmt"

	"github.com/maxid/gnety/codec/modbus"
)

// Sentinels returned by the client. They are wrapped in a longer message, so
// callers test them with errors.Is.
var (
	// ErrClosed is returned when the client has no connection: Close was called,
	// or an exchange ran out of attempts and dropped the connection.
	ErrClosed = errors.New("gmodbus: client is closed")
	// ErrTimeout is returned when every attempt timed out without a response.
	ErrTimeout = errors.New("gmodbus: request timed out")
	// ErrTransactionMismatch is returned when a response carries a different
	// transaction id or unit id than the request it claims to answer.
	ErrTransactionMismatch = errors.New("gmodbus: response does not match the request")
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
