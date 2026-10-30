package gmodbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"
)

// dial opens one transport connection. A configured Dialer owns the transport
// completely; otherwise a plain TCP connection is dialed and, when TLS is
// configured, wrapped in a TLS client.
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	if c.isClosed() {
		return nil, ErrClosed
	}

	// A Close during a dial cancels it even if the caller's context is still
	// live. AfterFunc only starts a goroutine when closeCtx is actually done.
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.closeCtx, cancel)
	defer stop()

	if c.dialer != nil {
		conn, err := c.dialer(dialCtx, "tcp", c.addr)
		if err != nil {
			return nil, fmt.Errorf("gmodbus: dial %s: %w", c.addr, err)
		}
		if conn == nil {
			return nil, fmt.Errorf("gmodbus: dial %s: custom Dialer returned a nil connection", c.addr)
		}
		return conn, nil
	}

	d := &net.Dialer{Timeout: c.dialTimeout}
	conn, err := d.DialContext(dialCtx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("gmodbus: dial %s: %w", c.addr, err)
	}
	if c.tlsConfig == nil {
		return conn, nil
	}
	return c.tlsDial(dialCtx, conn)
}

// tlsDial completes the TLS handshake within DialTimeout (or the caller's
// earlier deadline) and classifies a failure so a reconnect can tell a bad
// configuration from a transient outage.
func (c *Client) tlsDial(parent context.Context, conn net.Conn) (net.Conn, error) {
	deadline := time.Now().Add(c.dialTimeout)
	if d, ok := parent.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	handshakeCtx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()

	// HandshakeContext honors cancellation, but the underlying connection also
	// gets the deadline so a peer that stops reading cannot hold it open.
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: dial %s: TLS deadline: %w", ErrTLSHandshake, c.addr, err)
	}
	tlsConn := tls.Client(conn, c.tlsConfig)
	err := tlsConn.HandshakeContext(handshakeCtx)
	if err == nil {
		err = conn.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = conn.Close()
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		return nil, classifyTLSHandshake(c.addr, err)
	}
	return tlsConn, nil
}

// classifyTLSHandshake wraps a failed handshake with the sentinel that says
// whether a retry can help.
func classifyTLSHandshake(addr string, err error) error {
	permanent := tlsErrorIsPermanent(err)
	if permanent {
		return fmt.Errorf("%w: dial %s: TLS handshake: %w", ErrTLSConfig, addr, err)
	}
	return fmt.Errorf("%w: dial %s: TLS handshake: %w", ErrTLSHandshake, addr, err)
}

// tlsErrorIsPermanent reports whether a handshake failure is a configuration
// problem that a fresh connection cannot fix: certificate verification, a peer
// that does not speak TLS, or a TLS alert such as a protocol mismatch.
func tlsErrorIsPermanent(err error) bool {
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalidCert      x509.CertificateInvalidError
		verify           *tls.CertificateVerificationError
		recordHeader     tls.RecordHeaderError
		alert            tls.AlertError
	)
	switch {
	case errors.As(err, &unknownAuthority),
		errors.As(err, &hostname),
		errors.As(err, &invalidCert),
		errors.As(err, &verify),
		errors.As(err, &recordHeader),
		errors.As(err, &alert):
		return true
	default:
		return false
	}
}

// isTLSConfigError reports whether err is a permanent TLS configuration
// failure, which reconnection must not retry.
func isTLSConfigError(err error) bool { return errors.Is(err, ErrTLSConfig) }
