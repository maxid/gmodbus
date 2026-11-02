// Package gmodbus is a Modbus client and server for Go.
//
// Seven transports are supported: TCP, TLS over TCP, UDP, DTLS, RTU over a
// serial line, RTU over TCP and RTU over UDP. The wire formats (MBAP for the
// network transports, the CRC-framed RTU form for the serial family) live in
// github.com/maxid/gnety/codec/modbus; this package owns the connection, the
// transaction, the retries, the exception mapping, the polling schedule and the
// server dispatch.
//
// # Client
//
// NewClient opens one connection for the configured transport. The typed
// operations cover function codes 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x0F and
// 0x10, plus 0x16 mask write, 0x17 read/write multiple and 0x2B/0x0E device
// identification. Execute sends any type that implements modbus.Message, which
// is how a vendor function code registered in ClientConfig.Functions is used.
//
// One request is in flight per client: concurrent callers are serialized on the
// wire and wait for the current exchange or for their own context. Timeout
// bounds one attempt, Retries bounds extra attempts after a timeout or transport
// failure, and a Modbus exception response is a delivered answer that is never
// retried.
//
// # Server
//
// NewServer binds the configured transport and serves requests until Close or
// the context ends. A Handler may take over any request; when it returns
// ErrUnhandled the built-in MemoryStore answers instead. The store keeps an
// independent set of coils, discrete inputs, holding registers and input
// registers per unit id. On the RTU transports unit 0 is a broadcast: a write is
// applied without a response, and a read is ignored.
//
// # Vendor function codes
//
// ServerConfig.Functions and ClientConfig.Functions share one registry from
// gnety/codec/modbus: a request and response type registered for a function
// code are decoded instead of the built-in one, including a vendor redefinition
// of a standard code.
package gmodbus

import ()

// Network selects a client or server transport.
type Network string

const (
	// NetworkTCP is Modbus TCP: MBAP frames over one TCP connection.
	NetworkTCP Network = "tcp"
	// NetworkTLS is Modbus TCP over TLS. TLS is required in ClientConfig or
	// ServerConfig.
	NetworkTLS Network = "tls"
	// NetworkUDP is MBAP frames one per UDP datagram.
	NetworkUDP Network = "udp"
	// NetworkDTLS is MBAP frames one per DTLS record. DTLS is required in
	// ClientConfig or ServerConfig.
	NetworkDTLS Network = "dtls"
	// NetworkRTU is the CRC-framed RTU form over a serial line; Addr is the
	// device path.
	NetworkRTU Network = "rtu"
	// NetworkRTUOverTCP is the CRC-framed RTU form over TCP.
	NetworkRTUOverTCP Network = "rtu-tcp"
	// NetworkRTUOverUDP is the CRC-framed RTU form, one frame per UDP datagram.
	NetworkRTUOverUDP Network = "rtu-udp"
)

// valid reports whether n names a transport this package implements.
func (n Network) valid() bool {
	switch n {
	case NetworkTCP, NetworkTLS, NetworkUDP, NetworkDTLS, NetworkRTU, NetworkRTUOverTCP, NetworkRTUOverUDP:
		return true
	default:
		return false
	}
}

// datagram reports whether one request or response occupies one datagram.
func (n Network) datagram() bool {
	return n == NetworkUDP || n == NetworkDTLS || n == NetworkRTUOverUDP
}

// rtuFamily reports whether the transport uses the CRC-framed RTU form.
func (n Network) rtuFamily() bool {
	return n == NetworkRTU || n == NetworkRTUOverTCP || n == NetworkRTUOverUDP
}

// serial reports whether the transport is a serial line.
func (n Network) serial() bool { return n == NetworkRTU }

// Parity is a serial line parity setting.
type Parity uint8

// Serial line parity values.
const (
	NoParity Parity = iota
	OddParity
	EvenParity
	MarkParity
	SpaceParity
)

// StopBits is the number of stop bits on a serial line.
type StopBits uint8

// Serial line stop bit values.
const (
	OneStopBit  StopBits = 1
	TwoStopBits StopBits = 2
)

// SerialConfig configures the line parameters of NetworkRTU. The zero value
// means 19200 baud, 8 data bits, one stop bit and no parity.
//
// Note that the Modbus serial specification mandates two stop bits when the
// line runs without parity; many devices use 8N1 instead, so the zero value
// stays at one stop bit and the caller chooses TwoStopBits explicitly.
type SerialConfig struct {
	// Baud is the bit rate, for example 9600 or 19200.
	Baud int
	// DataBits is the character size, 5 to 8.
	DataBits int
	// StopBits is one or two stop bits.
	StopBits StopBits
	// Parity is the parity setting.
	Parity Parity
}
