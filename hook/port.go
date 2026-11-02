package hook

import (
	"io"
	"time"
)

// Port is a serial line supplied by the caller instead of one opened from
// ClientConfig.Addr or ServerConfig.Addr. net.Conn satisfies it, so a test can
// use net.Pipe and a platform with its own serial stack can supply its own
// implementation.
type Port interface {
	io.ReadWriteCloser
	// SetDeadline bounds the next reads and writes; a zero time clears it.
	SetDeadline(time.Time) error
}
