//go:build integration

package interop

// The reference-implementation matrix: pymodbus, the Python Modbus stack, on
// both sides of this library.
//
//	direction A: our client drives a pymodbus slave running in a container;
//	direction B: pymodbus's own client drives our server, invoked from the same
//	             image and reaching the host through host.docker.internal.
//
// Both directions need Docker, so the file carries the integration tag and every
// case skips with the reason when no daemon answers. The image is built from
// interop/docker/pymodbus and cached under a version-pinned tag.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/maxid/gmodbus"
)

const (
	// pymodbusImage is pinned to the pymodbus release the scripts were written
	// against, because its client and datastore APIs move between releases.
	pymodbusImage = "gmodbus-interop-pymodbus:3.8.6"
	// pymodbusBuildContext is the directory holding the Dockerfile and the two
	// scripts, relative to this module.
	pymodbusBuildContext = "docker/pymodbus"
	// pymodbusPort is the slave's listener inside the container; the host port is
	// picked fresh per run.
	pymodbusPort = "5020"
	// exchangeTimeout bounds one Modbus exchange of either direction.
	exchangeTimeout = 5 * time.Second
	// referenceClientTimeout bounds one run of the reference client container,
	// which includes starting it.
	referenceClientTimeout = 60 * time.Second
)

// TestPymodbusSlave drives the reference slave with our client: the four data
// banks, both write forms, and an address the slave does not hold.
func TestPymodbusSlave(t *testing.T) {
	requireDocker(t)
	ensureImage(t, pymodbusImage, pymodbusBuildContext)

	port := freePort(t)
	addr := net.JoinHostPort("127.0.0.1", port)
	name := containerName()
	t.Cleanup(func() { removeContainer(t, name) })

	out, err := runDocker(dockerStartTimeout, "run", "-d", "--rm",
		"--name", name,
		"-p", addr+":"+pymodbusPort,
		pymodbusImage)
	if err != nil {
		t.Skipf("the reference slave container could not be started: %v", err)
	}
	t.Logf("reference slave container %s (id %.12s) is mapped to %s", name, string(out), addr)
	waitExchange(t, name, addr, containerReadyTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cli, err := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
		Network: gmodbus.NetworkTCP,
		Addr:    addr,
		Timeout: exchangeTimeout,
	})
	if err != nil {
		t.Fatalf("our client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	// The values are the ones slave.py seeds; the pairing is the point of the
	// test, so each read asserts them exactly.
	holding, err := cli.ReadHoldingRegisters(ctx, 1, 0, 4)
	if err != nil || !slices.Equal(holding, []uint16{10, 20, 30, 40}) {
		t.Fatalf("ReadHoldingRegisters = %v, %v, want [10 20 30 40]", holding, err)
	}
	input, err := cli.ReadInputRegisters(ctx, 1, 0, 4)
	if err != nil || !slices.Equal(input, []uint16{100, 200, 300, 400}) {
		t.Fatalf("ReadInputRegisters = %v, %v, want [100 200 300 400]", input, err)
	}
	discrete, err := cli.ReadDiscreteInputs(ctx, 1, 0, 3)
	if err != nil || !slices.Equal(discrete, []bool{true, false, true}) {
		t.Fatalf("ReadDiscreteInputs = %v, %v, want [true false true]", discrete, err)
	}
	coils, err := cli.ReadCoils(ctx, 1, 0, 4)
	if err != nil || !slices.Equal(coils, []bool{true, false, true, false}) {
		t.Fatalf("ReadCoils = %v, %v, want [true false true false]", coils, err)
	}

	if err := cli.WriteSingleRegister(ctx, 1, 2, 0x1234); err != nil {
		t.Fatalf("WriteSingleRegister: %v", err)
	}
	if got, err := cli.ReadHoldingRegisters(ctx, 1, 2, 1); err != nil || !slices.Equal(got, []uint16{0x1234}) {
		t.Fatalf("holding 2 after the write = %v, %v, want [0x1234]", got, err)
	}
	if err := cli.WriteMultipleCoils(ctx, 1, 4, []bool{true, false, true}); err != nil {
		t.Fatalf("WriteMultipleCoils: %v", err)
	}
	if got, err := cli.ReadCoils(ctx, 1, 4, 3); err != nil || !slices.Equal(got, []bool{true, false, true}) {
		t.Fatalf("coils 4..6 after the write = %v, %v", got, err)
	}
	if err := cli.WriteMultipleRegisters(ctx, 1, 8, []uint16{7, 8}); err != nil {
		t.Fatalf("WriteMultipleRegisters: %v", err)
	}
	if got, err := cli.ReadHoldingRegisters(ctx, 1, 8, 2); err != nil || !slices.Equal(got, []uint16{7, 8}) {
		t.Fatalf("holding 8..9 after the write = %v, %v", got, err)
	}

	// An address the slave does not serve is an exception response, which is a
	// delivered answer: it must arrive as the typed error, not as a timeout.
	_, err = cli.ReadHoldingRegisters(ctx, 1, 2000, 2)
	var rejected *gmodbus.Error
	if !errors.As(err, &rejected) {
		t.Fatalf("read of an unheld address = %v, want a *gmodbus.Error", err)
	}
	if rejected.Code != 2 {
		t.Fatalf("exception code = %d, want 2 (illegal data address)", rejected.Code)
	}
}

// TestPymodbusMaster runs the reference client against our server and asserts on
// what it reports, including that an address outside the bank comes back as an
// exception rather than a timeout or a silent zero.
func TestPymodbusMaster(t *testing.T) {
	requireDocker(t)
	ensureImage(t, pymodbusImage, pymodbusBuildContext)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srv, err := gmodbus.NewServer(ctx, gmodbus.ServerConfig{
		Network: gmodbus.NetworkTCP,
		// The container reaches the host over the docker bridge, so the server has
		// to listen on every interface rather than loopback only.
		Addr:  "0.0.0.0:0",
		Store: seededStore(t),
		Device: &gmodbus.DeviceIdentification{
			VendorName:  "gmodbus interop",
			ProductCode: "INTEROP-1",
		},
	})
	if err != nil {
		t.Fatalf("our server: %v", err)
	}
	defer func() { _ = srv.Close() }()
	_, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("server address %q: %v", srv.Addr(), err)
	}
	t.Logf("our server is reachable from the container as %s:%s", hostAlias, port)

	out, err := runDocker(referenceClientTimeout,
		"run", "--rm",
		"--add-host", hostAlias+":host-gateway",
		"--entrypoint", "python",
		pymodbusImage,
		"/interop/master.py", hostAlias, port)
	if err != nil {
		t.Fatalf("the reference client failed: %v", err)
	}

	// master.py prints one JSON object and exits non-zero on a failed exchange.
	// pymodbus's own log shares the output stream docker hands back, so the report
	// is the last line that parses as an object.
	var got struct {
		Coils            []bool   `json:"coils"`
		DiscreteInputs   []bool   `json:"discrete_inputs"`
		Holding          []uint16 `json:"holding"`
		Input            []uint16 `json:"input"`
		WrittenRegister  []uint16 `json:"written_register"`
		WrittenCoils     []bool   `json:"written_coils"`
		WrittenRegisters []uint16 `json:"written_registers"`
		Exception        bool     `json:"exception"`
		ExceptionCode    *int     `json:"exception_code"`
	}
	report, err := lastJSONLine(out)
	if err != nil {
		t.Fatalf("parse the reference client's report: %v\n%s", err, out)
	}
	if err := json.Unmarshal(report, &got); err != nil {
		t.Fatalf("parse the reference client's report: %v\n%s", err, report)
	}
	if !slices.Equal(got.Coils, []bool{true, false, true}) {
		t.Errorf("reference read coils = %v, want [true false true]", got.Coils)
	}
	if !slices.Equal(got.DiscreteInputs, []bool{true, false, true}) {
		t.Errorf("reference read discrete inputs = %v, want [true false true]", got.DiscreteInputs)
	}
	if !slices.Equal(got.Holding, []uint16{10, 20, 30, 40}) {
		t.Errorf("reference read holding registers = %v, want [10 20 30 40]", got.Holding)
	}
	if !slices.Equal(got.Input, []uint16{100, 200, 300, 400}) {
		t.Errorf("reference read input registers = %v, want [100 200 300 400]", got.Input)
	}
	if !slices.Equal(got.WrittenRegister, []uint16{0x1234}) {
		t.Errorf("reference read back the written register = %v, want [0x1234]", got.WrittenRegister)
	}
	if !slices.Equal(got.WrittenCoils, []bool{true, false, true}) {
		t.Errorf("reference read back the written coils = %v, want [true false true]", got.WrittenCoils)
	}
	if !slices.Equal(got.WrittenRegisters, []uint16{7, 8}) {
		t.Errorf("reference read back the written registers = %v, want [7 8]", got.WrittenRegisters)
	}
	if !got.Exception {
		t.Error("the reference client did not see an exception for an address outside the bank")
	}
	if got.ExceptionCode != nil && *got.ExceptionCode != 2 {
		t.Errorf("exception code = %d, want 2 (illegal data address)", *got.ExceptionCode)
	}
}

// seededStore is the data model both directions assert on. The values match the
// ones the examples and slave.py use, so one set of numbers covers the matrix.
func seededStore(t *testing.T) *gmodbus.MemoryStore {
	t.Helper()
	store := gmodbus.NewMemoryStore(nil)
	unit := store.AddUnit(1, gmodbus.UnitConfig{Coils: 16, DiscreteInputs: 16, HoldingRegisters: 16, InputRegisters: 16})
	if err := unit.SetCoils(0, []bool{true, false, true, false}); err != nil {
		t.Fatalf("seed coils: %v", err)
	}
	if err := unit.SetDiscreteInputs(0, []bool{true, false, true}); err != nil {
		t.Fatalf("seed discrete inputs: %v", err)
	}
	if err := unit.SetHoldingRegisters(0, []uint16{10, 20, 30, 40}); err != nil {
		t.Fatalf("seed holding registers: %v", err)
	}
	if err := unit.SetInputRegisters(0, []uint16{100, 200, 300, 400}); err != nil {
		t.Fatalf("seed input registers: %v", err)
	}
	return store
}

// lastJSONLine returns the last line of out that parses as a JSON object, which
// is how the reference client's report is found among its own log lines.
func lastJSONLine(out []byte) ([]byte, error) {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' || !json.Valid(line) {
			continue
		}
		return line, nil
	}
	return nil, errors.New("no JSON object in the output")
}
