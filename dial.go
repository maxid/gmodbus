package gmodbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/maxid/gnety"
	"github.com/pion/dtls/v2"
)

// open opens one transport connection and pairs it with the codec for its
// framing. It is called from the single exchange slot, so it never races with
// another open on the same client.
func (c *Client) open(ctx context.Context) (*clientLink, error) {
	switch c.network {
	case NetworkTCP:
		return c.openStream(ctx, "tcp", nil, false)
	case NetworkTLS:
		return c.openStream(ctx, "tcp-tls", c.tlsConfig, false)
	case NetworkRTUOverTCP:
		return c.openStream(ctx, "tcp", nil, true)
	case NetworkUDP:
		return c.openDatagram(ctx, "udp", nil, false)
	case NetworkDTLS:
		return c.openDatagram(ctx, "dtls", c.dtlsConfig, false)
	case NetworkRTUOverUDP:
		return c.openDatagram(ctx, "udp", nil, true)
	case NetworkRTU:
		return c.openSerial(ctx)
	default:
		return nil, fmt.Errorf("gmodbus: unsupported network %q", c.network)
	}
}

// openStream opens a TCP or TLS connection and installs the MBAP or RTU stream
// codec.
func (c *Client) openStream(ctx context.Context, network string, tlsCfg *tls.Config, rtuMode bool) (*clientLink, error) {
	codec := &streamCodec{
		rtuMode:   rtuMode,
		rtu:       c.rtu,
		functions: c.functions,
		maxPDU:    c.maxPDU,
	}
	cfg := gnety.ClientConfig{
		Network: network,
		Addr:    c.addr,
		Timeout: c.dialTimeout,
		Codec:   codec,
	}
	if tlsCfg != nil {
		cfg.TLSConfig = tlsCfg
	}
	dialCtx, cancel := c.connectContext(ctx)
	defer cancel()
	cli, err := gnety.Dial(dialCtx, cfg)
	if err != nil {
		return nil, c.classifyConnectError(err)
	}
	return &clientLink{cli: cli, codec: codec}, nil
}

// openDatagram opens a UDP or DTLS connection and installs the MBAP or RTU
// datagram codec.
func (c *Client) openDatagram(ctx context.Context, network string, dtlsCfg *dtls.Config, rtuMode bool) (*clientLink, error) {
	codec := &datagramCodec{
		rtuMode:   rtuMode,
		rtu:       c.rtu,
		functions: c.functions,
		maxPDU:    c.maxPDU,
	}
	cfg := gnety.ClientConfig{
		Network:       network,
		Addr:          c.addr,
		Timeout:       c.dialTimeout,
		DatagramCodec: codec,
	}
	if dtlsCfg != nil {
		cfg.DTLSConfig = dtlsCfg
	}
	dialCtx, cancel := c.connectContext(ctx)
	defer cancel()
	cli, err := gnety.Dial(dialCtx, cfg)
	if err != nil {
		return nil, c.classifyConnectError(err)
	}
	return &clientLink{cli: cli, codec: codec}, nil
}

// openSerial opens the serial line and wraps it as a gnety client. Serial is the
// one transport gnety does not open itself; the custom-socket entry point keeps
// the codec and read/write path identical to the network transports.
func (c *Client) openSerial(ctx context.Context) (*clientLink, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	port, err := c.openPort()
	if err != nil {
		return nil, err
	}
	codec := &streamCodec{
		rtuMode:   true,
		rtu:       c.rtu,
		functions: c.functions,
		maxPDU:    c.maxPDU,
	}
	cli, err := gnety.NewClient(newPortConn(port, c.addr), gnety.ClientConfig{
		Network: "tcp",
		Addr:    c.addr,
		Codec:   codec,
	})
	if err != nil {
		_ = port.Close()
		return nil, err
	}
	return &clientLink{cli: cli, codec: codec}, nil
}

// openPort returns the caller-supplied port or opens the configured serial
// device.
func (c *Client) openPort() (Port, error) {
	if c.port != nil {
		return c.port, nil
	}
	return openSerialPort(c.addr, c.serial)
}

// connectContext bounds one connect by DialTimeout or the caller's earlier
// deadline.
func (c *Client) connectContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(c.dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	return context.WithDeadline(ctx, deadline)
}

// classifyConnectError marks a permanent TLS/DTLS configuration failure so a
// reconnect does not spend its attempt budget on it. Every other connect error
// is transient by nature.
func (c *Client) classifyConnectError(err error) error {
	if err == nil {
		return nil
	}
	if isTLSConfigError(err) || !tlsErrorIsPermanent(err) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrTLSConfig, err)
}

// tlsErrorIsPermanent reports whether a handshake failure is a configuration
// problem that a fresh connection cannot fix: certificate verification, a peer
// that does not speak TLS or DTLS, or a security alert such as a protocol
// mismatch.
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

// isTLSConfigError reports whether err is a permanent TLS/DTLS configuration
// failure, which reconnection must not retry.
func isTLSConfigError(err error) bool { return errors.Is(err, ErrTLSConfig) }
