package gmodbus

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/maxid/gnety/codec/modbus"
	"github.com/pion/dtls/v2"
)

// startMatrix starts a server for network and returns a constructor for a
// matching client. The serial network is bridged with net.Pipe, so the RTU
// framing and the client/server loops run without hardware.
func startMatrix(t *testing.T, network Network) (*Server, func(t *testing.T) *Client) {
	t.Helper()
	cert, pool := testCertificate(t)
	serverCfg := ServerConfig{
		Network: network,
		Store:   NewMemoryStore(nil),
		Device: &DeviceIdentification{
			VendorName:         "ACME",
			ProductCode:        "GM-1",
			MajorMinorRevision: "1.0",
		},
	}
	clientCfg := ClientConfig{
		Network:     network,
		DialTimeout: 2 * time.Second,
		Timeout:     2 * time.Second,
	}
	if !network.serial() {
		serverCfg.Addr = "127.0.0.1:0"
	}
	switch network {
	case NetworkTLS:
		serverCfg.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		clientCfg.TLS = &tls.Config{RootCAs: pool}
	case NetworkDTLS:
		serverCfg.DTLS = &dtls.Config{Certificates: []tls.Certificate{cert}}
		clientCfg.DTLS = &dtls.Config{RootCAs: pool}
	case NetworkRTU:
		serverPort, clientPort := net.Pipe()
		serverCfg.Port = serverPort
		clientCfg.Port = clientPort
	}
	srv, err := NewServer(context.Background(), serverCfg)
	if err != nil {
		t.Fatalf("NewServer(%s): %v", network, err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if !network.serial() {
		clientCfg.Addr = srv.Addr()
	}
	return srv, func(t *testing.T) *Client {
		t.Helper()
		cli, err := NewClient(context.Background(), clientCfg)
		if err != nil {
			t.Fatalf("NewClient(%s): %v", network, err)
		}
		t.Cleanup(func() { _ = cli.Close() })
		return cli
	}
}

func TestTransportMatrix(t *testing.T) {
	for _, network := range []Network{NetworkTCP, NetworkTLS, NetworkUDP, NetworkDTLS, NetworkRTU, NetworkRTUOverTCP, NetworkRTUOverUDP} {
		t.Run(string(network), func(t *testing.T) {
			srv, newClient := startMatrix(t, network)
			unit, ok := srv.store.Unit(1)
			if !ok {
				t.Fatal("server store has no unit 1")
			}
			if err := unit.SetDiscreteInputs(0, []bool{true, false, true}); err != nil {
				t.Fatal(err)
			}
			if err := unit.SetInputRegisters(0, []uint16{10, 11, 12}); err != nil {
				t.Fatal(err)
			}
			cli := newClient(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			coils, err := cli.ReadCoils(ctx, 1, 0, 4)
			if err != nil {
				t.Fatalf("ReadCoils: %v", err)
			}
			if len(coils) != 4 || coils[0] {
				t.Fatalf("coils = %v", coils)
			}
			inputs, err := cli.ReadDiscreteInputs(ctx, 1, 0, 3)
			if err != nil {
				t.Fatalf("ReadDiscreteInputs: %v", err)
			}
			if want := []bool{true, false, true}; !equalBool(inputs, want) {
				t.Fatalf("discrete inputs = %v, want %v", inputs, want)
			}
			registers, err := cli.ReadInputRegisters(ctx, 1, 0, 3)
			if err != nil {
				t.Fatalf("ReadInputRegisters: %v", err)
			}
			if want := []uint16{10, 11, 12}; !equalUint16(registers, want) {
				t.Fatalf("input registers = %v, want %v", registers, want)
			}

			if err := cli.WriteSingleCoil(ctx, 1, 2, true); err != nil {
				t.Fatalf("WriteSingleCoil: %v", err)
			}
			coils, err = cli.ReadCoils(ctx, 1, 2, 1)
			if err != nil || !coils[0] {
				t.Fatalf("coil 2 = %v, err %v", coils, err)
			}
			if err := cli.WriteSingleRegister(ctx, 1, 0, 7); err != nil {
				t.Fatalf("WriteSingleRegister: %v", err)
			}
			if err := cli.WriteMultipleCoils(ctx, 1, 4, []bool{true, true, false}); err != nil {
				t.Fatalf("WriteMultipleCoils: %v", err)
			}
			if err := cli.WriteMultipleRegisters(ctx, 1, 2, []uint16{5, 6}); err != nil {
				t.Fatalf("WriteMultipleRegisters: %v", err)
			}
			if err := cli.MaskWriteRegister(ctx, 1, 0, 0xff00, 0x00ff); err != nil {
				t.Fatalf("MaskWriteRegister: %v", err)
			}
			if value, err := unit.HoldingRegister(0); err != nil || value != 0x00ff {
				t.Fatalf("register 0 = %#04x, err %v", value, err)
			}
			values, err := cli.ReadWriteMultipleRegisters(ctx, 1, 3, 1, 3, []uint16{9})
			if err != nil {
				t.Fatalf("ReadWriteMultipleRegisters: %v", err)
			}
			if want := []uint16{9}; !equalUint16(values, want) {
				t.Fatalf("read/write multiple = %v, want %v", values, want)
			}

			identity, err := cli.ReadDeviceIdentification(ctx, 1, modbus.DeviceIDBasic, 0)
			if err != nil {
				t.Fatalf("ReadDeviceIdentification: %v", err)
			}
			if len(identity.Objects) == 0 || string(identity.Objects[0].Value) != "ACME" {
				t.Fatalf("device identification = %#v", identity)
			}

			_, err = cli.ReadHoldingRegisters(ctx, 1, 5000, 1)
			var rejected *Error
			if !errors.As(err, &rejected) || rejected.Code != modbus.ExceptionIllegalDataAddress {
				t.Fatalf("out-of-range read = %v, want IllegalDataAddress", err)
			}
		})
	}
}

func TestRTUBroadcastWritesToEveryUnit(t *testing.T) {
	for _, network := range []Network{NetworkRTU, NetworkRTUOverTCP, NetworkRTUOverUDP} {
		t.Run(string(network), func(t *testing.T) {
			srv, newClient := startMatrix(t, network)
			unitTwo := srv.store.AddUnit(2, UnitConfig{})
			wrote := make(chan WriteEvent, 4)
			srv.store.OnWrite(func(event WriteEvent) { wrote <- event })
			cli := newClient(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := cli.WriteSingleRegister(ctx, 0, 3, 42); err != nil {
				t.Fatalf("broadcast write: %v", err)
			}
			// A broadcast has no reply, so observe the store's write events instead
			// of racing the server loop.
			for i := 0; i < 2; i++ {
				select {
				case <-wrote:
				case <-ctx.Done():
					t.Fatal("broadcast write was not applied")
				}
			}
			for _, unit := range []*UnitStore{srv.store.unitFor(1), unitTwo} {
				if value, err := unit.HoldingRegister(3); err != nil || value != 42 {
					t.Fatalf("unit %d register 3 = %d, err %v", unit.Unit(), value, err)
				}
			}
			if _, err := cli.ReadHoldingRegisters(ctx, 0, 3, 1); err == nil {
				t.Fatal("broadcast read did not fail")
			}
		})
	}
}

// TestCustomFunctionAcrossTransports registers a vendor function code and runs
// it end to end over TCP and RTU-over-TCP, including the RTU frame-length hooks.
func TestCustomFunctionAcrossTransports(t *testing.T) {
	custom := modbus.Functions{
		0x64: {
			DecodeRequest: func(pdu []byte) (modbus.Message, error) {
				return &customRequest{Address: uint16(pdu[1])<<8 | uint16(pdu[2])}, nil
			},
			DecodeResponse: func(req modbus.Message, pdu []byte) (modbus.Message, error) {
				value := uint16(pdu[1])<<8 | uint16(pdu[2])
				return &customResponse{Value: value}, nil
			},
			RequestLength:  func([]byte) (int, error) { return 3, nil },
			ResponseLength: func(modbus.Message, []byte) (int, error) { return 3, nil },
		},
	}
	for _, network := range []Network{NetworkTCP, NetworkRTUOverTCP, NetworkRTU} {
		t.Run(string(network), func(t *testing.T) {
			serverCfg := ServerConfig{
				Network:   network,
				Store:     NewMemoryStore(nil),
				Functions: custom,
				Handler: HandlerFunc(func(ctx context.Context, unit uint8, req modbus.Message) (modbus.Message, error) {
					value, ok := req.(*customRequest)
					if !ok {
						return nil, ErrUnhandled
					}
					return &customResponse{Value: value.Address + 1}, nil
				}),
			}
			clientCfg := ClientConfig{
				Network:     network,
				DialTimeout: 2 * time.Second,
				Timeout:     2 * time.Second,
				Functions:   custom,
			}
			if network == NetworkRTU {
				serverPort, clientPort := net.Pipe()
				serverCfg.Port = serverPort
				clientCfg.Port = clientPort
			} else {
				serverCfg.Addr = "127.0.0.1:0"
			}
			srv, err := NewServer(context.Background(), serverCfg)
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			defer func() { _ = srv.Close() }()
			if !network.serial() {
				clientCfg.Addr = srv.Addr()
			}
			cli, err := NewClient(context.Background(), clientCfg)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			defer func() { _ = cli.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := cli.Execute(ctx, 1, &customRequest{Address: 0x1234})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			got, ok := resp.(*customResponse)
			if !ok || got.Value != 0x1235 {
				t.Fatalf("response = %#v", resp)
			}
		})
	}
}

// customRequest is a vendor function code 0x64 request: a 16-bit address.
type customRequest struct {
	Address uint16
}

func (r *customRequest) Function() byte { return 0x64 }

func (r *customRequest) PDU() ([]byte, error) {
	return []byte{0x64, byte(r.Address >> 8), byte(r.Address)}, nil
}

// customResponse answers a customRequest.
type customResponse struct {
	Value uint16
}

func (r *customResponse) Function() byte { return 0x64 }

func (r *customResponse) PDU() ([]byte, error) {
	return []byte{0x64, byte(r.Value >> 8), byte(r.Value)}, nil
}

func equalBool(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalUint16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
