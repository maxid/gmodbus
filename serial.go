package gmodbus

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"go.bug.st/serial"
)

// Default serial line settings used when SerialConfig leaves a field zero.
const (
	DefaultSerialBaud     = 19200
	DefaultSerialDataBits = 8
)

// normalizeSerial validates a serial configuration and fills its defaults. It
// always returns a non-nil configuration, so callers never branch on nil.
func normalizeSerial(in *SerialConfig) (*SerialConfig, error) {
	out := SerialConfig{
		Baud:     DefaultSerialBaud,
		DataBits: DefaultSerialDataBits,
		StopBits: OneStopBit,
		Parity:   NoParity,
	}
	if in != nil {
		out = *in
	}
	if out.Baud < 0 {
		return nil, fmt.Errorf("gmodbus: Serial.Baud %d is negative", out.Baud)
	}
	if out.Baud == 0 {
		out.Baud = DefaultSerialBaud
	}
	if out.DataBits < 0 || out.DataBits > 0 && (out.DataBits < 5 || out.DataBits > 8) {
		return nil, fmt.Errorf("gmodbus: Serial.DataBits %d is outside 5..8", out.DataBits)
	}
	if out.DataBits == 0 {
		out.DataBits = DefaultSerialDataBits
	}
	switch out.StopBits {
	case 0:
		out.StopBits = OneStopBit
	case OneStopBit, TwoStopBits:
	default:
		return nil, fmt.Errorf("gmodbus: Serial.StopBits %d is neither 1 nor 2", out.StopBits)
	}
	if out.Parity > SpaceParity {
		return nil, fmt.Errorf("gmodbus: Serial.Parity %d is unknown", out.Parity)
	}
	return &out, nil
}

// openSerialPort opens the serial device and adapts it to the Port interface.
func openSerialPort(path string, cfg SerialConfig) (Port, error) {
	port, err := serial.Open(path, &serial.Mode{
		BaudRate: cfg.Baud,
		DataBits: cfg.DataBits,
		Parity:   serialParity(cfg.Parity),
		StopBits: serial.StopBits(cfg.StopBits),
	})
	if err != nil {
		return nil, fmt.Errorf("gmodbus: open serial %s: %w", path, err)
	}
	return &serialPort{port: port}, nil
}

// serialParity maps the public parity values to the serial package's.
func serialParity(parity Parity) serial.Parity {
	switch parity {
	case OddParity:
		return serial.OddParity
	case EvenParity:
		return serial.EvenParity
	case MarkParity:
		return serial.MarkParity
	case SpaceParity:
		return serial.SpaceParity
	default:
		return serial.NoParity
	}
}

// serialPort adapts serial.Port to the Port interface. The serial package only
// supports a read timeout, so a write deadline is remembered but cannot be
// enforced; the client's attempt deadline still bounds the read that follows.
type serialPort struct {
	port serial.Port

	mu       sync.Mutex
	deadline time.Time
}

func (p *serialPort) Read(b []byte) (int, error) {
	p.mu.Lock()
	deadline := p.deadline
	p.mu.Unlock()

	timeout := serial.NoTimeout
	if !deadline.IsZero() {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
	}
	if err := p.port.SetReadTimeout(timeout); err != nil {
		return 0, err
	}
	n, err := p.port.Read(b)
	if err != nil {
		return n, err
	}
	if n == 0 {
		// The unix backend reports a read timeout as (0, nil). A read with no
		// deadline blocks instead of returning zero bytes, so this is a timeout.
		return 0, os.ErrDeadlineExceeded
	}
	return n, nil
}

func (p *serialPort) Write(b []byte) (int, error) { return p.port.Write(b) }

func (p *serialPort) Close() error { return p.port.Close() }

func (p *serialPort) SetDeadline(t time.Time) error {
	p.mu.Lock()
	p.deadline = t
	p.mu.Unlock()
	return nil
}

// portConn adapts a Port to net.Conn so gnety can carry it like any other
// custom socket. The serial line has no meaningful addresses; the device path
// is reported for both.
type portConn struct {
	Port
	addr serialAddr
}

func newPortConn(port Port, path string) net.Conn {
	return &portConn{Port: port, addr: serialAddr(path)}
}

func (c *portConn) LocalAddr() net.Addr  { return c.addr }
func (c *portConn) RemoteAddr() net.Addr { return c.addr }

func (c *portConn) SetReadDeadline(t time.Time) error { return c.Port.SetDeadline(t) }

// SetWriteDeadline reports success without doing anything: the serial package
// has no write timeout.
func (c *portConn) SetWriteDeadline(time.Time) error { return nil }

type serialAddr string

func (a serialAddr) Network() string { return "serial" }
func (a serialAddr) String() string  { return string(a) }
