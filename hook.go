package gmodbus

import "github.com/maxid/gmodbus/hook"

// The extension contracts live in ./hook: Handler (server request handling), Metrics and
// ServerMetrics (observability), Port (an externally opened serial line). Every name below
// is an alias, so a consumer keeps importing this package and still gets the hook type —
// one definition, two import paths. New code may import github.com/maxid/gmodbus/hook
// directly; both spellings are the same type.
type (
	Handler           = hook.Handler
	HandlerFunc       = hook.HandlerFunc
	Metrics           = hook.Metrics
	MetricsFunc       = hook.MetricsFunc
	NoopMetrics       = hook.NoopMetrics
	ServerMetrics     = hook.ServerMetrics
	ServerMetricsFunc = hook.ServerMetricsFunc
	NoopServerMetrics = hook.NoopServerMetrics
	Port              = hook.Port
)

const (
	ResultOK        = hook.ResultOK
	ResultException = hook.ResultException
	ResultTimeout   = hook.ResultTimeout
	ResultTransport = hook.ResultTransport
	ResultMismatch  = hook.ResultMismatch

	ServerResultOK        = hook.ServerResultOK
	ServerResultException = hook.ServerResultException
	ServerResultError     = hook.ServerResultError
)

// ErrUnhandled is returned by a Handler that does not want a request. It is the same
// sentinel as hook.ErrUnhandled, so errors.Is matches whichever import path you use.
var ErrUnhandled = hook.ErrUnhandled
