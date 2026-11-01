package gmodbus

import "time"

// Result labels handed to Metrics.Request. They are deliberately coarse: a
// metric never carries a unit id, an address, a register value or an error
// string.
const (
	// ResultOK is a request that was answered normally.
	ResultOK = "ok"
	// ResultException is a request the server answered with an exception
	// response. It is a delivered answer and is not retried.
	ResultException = "exception"
	// ResultTimeout is an attempt that got no complete response within Timeout.
	ResultTimeout = "timeout"
	// ResultTransport is an attempt that could not reach the server, lost the
	// connection, or received a frame the codec rejected. The connection is
	// dropped in every one of those cases.
	ResultTransport = "transport"
	// ResultMismatch is a response whose transaction id or unit id did not match
	// the request.
	ResultMismatch = "mismatch"
)

// Metrics receives one event per request attempt. Implementations must be safe
// for concurrent use and must not block: the hook runs on the caller's
// goroutine.
//
// Request is called once for every attempt the client makes, so a retried
// request reports its timeout and then its final result. function is the
// function code of the request and d is the wall time of that attempt.
//
// A nil Metrics in Config costs nothing: the client skips the clock read and
// the call.
type Metrics interface {
	Request(function byte, result string, d time.Duration)
}

// NoopMetrics drops every event.
type NoopMetrics struct{}

// Request implements Metrics.
func (NoopMetrics) Request(byte, string, time.Duration) {}

// record reports one attempt when a hook is configured.
func (c *Client) record(function byte, result string, start time.Time) {
	if c.metrics == nil {
		return
	}
	c.metrics.Request(function, result, time.Since(start))
}

// Server result labels handed to ServerMetrics.Request.
const (
	// ServerResultOK is a request the server answered normally.
	ServerResultOK = "ok"
	// ServerResultException is a request the server rejected with a Modbus
	// exception response.
	ServerResultException = "exception"
	// ServerResultError is a request whose handler failed or returned a response
	// the server could not use.
	ServerResultError = "error"
)

// ServerMetrics receives one event per served request. Implementations must be
// safe for concurrent use and must not block: the hook runs on the session
// goroutine.
//
// function is the request function code; result is one of ServerResultOK,
// ServerResultException or ServerResultError; d is the time the handler took.
// Like the client hook, it carries no unit id, address or value, so the labels
// stay low-cardinality. A nil ServerMetrics costs nothing.
type ServerMetrics interface {
	Request(function byte, result string, d time.Duration)
}

// NoopServerMetrics drops every event.
type NoopServerMetrics struct{}

// Request implements ServerMetrics.
func (NoopServerMetrics) Request(byte, string, time.Duration) {}
