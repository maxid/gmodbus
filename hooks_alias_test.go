package gmodbus

import (
	"errors"
	"testing"

	"github.com/maxid/gmodbus/hook"
)

// The extension contracts are defined in gmodbus/hook and re-exported here as aliases.
// These assertions fail to compile the moment one of them becomes a distinct named type,
// which is the compatibility promise this package makes: importing gmodbus and importing
// gmodbus/hook give you the same types.
var (
	_ hook.Handler           = (Handler)(nil)
	_ hook.HandlerFunc       = (HandlerFunc)(nil)
	_ hook.Metrics           = (Metrics)(nil)
	_ hook.MetricsFunc       = (MetricsFunc)(nil)
	_ hook.NoopMetrics       = NoopMetrics{}
	_ hook.ServerMetrics     = (ServerMetrics)(nil)
	_ hook.ServerMetricsFunc = (ServerMetricsFunc)(nil)
	_ hook.Port              = (Port)(nil)

	_ Handler       = hook.HandlerFunc(nil)
	_ Metrics       = hook.MetricsFunc(nil)
	_ ServerMetrics = hook.ServerMetricsFunc(nil)
)

func TestHookConstantsAndSentinelAreAliased(t *testing.T) {
	for _, pair := range [][2]string{
		{ResultOK, hook.ResultOK},
		{ResultException, hook.ResultException},
		{ResultTimeout, hook.ResultTimeout},
		{ResultTransport, hook.ResultTransport},
		{ResultMismatch, hook.ResultMismatch},
		{ServerResultOK, hook.ServerResultOK},
		{ServerResultException, hook.ServerResultException},
		{ServerResultError, hook.ServerResultError},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("constant mismatch: %q != %q", pair[0], pair[1])
		}
	}
	// The sentinel must be the same error value on both import paths, otherwise a
	// handler returning hook.ErrUnhandled would not be recognised by Server.dispatch.
	if !errors.Is(ErrUnhandled, hook.ErrUnhandled) || !errors.Is(hook.ErrUnhandled, ErrUnhandled) {
		t.Fatal("ErrUnhandled is not the same sentinel on both import paths")
	}
}
