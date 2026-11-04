package interop

// Shared helpers for the container-backed directions of the interoperability
// matrix. This file is not behind the integration build tag, so a plain
// `go vet ./...` and `go test ./...` compile it; nothing here runs unless an
// integration test calls it.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxid/gmodbus"
)

const (
	// containerPrefix names every container this suite creates, so a leftover is
	// recognizable and a human can find it with docker ps -a.
	containerPrefix = "gmodbus-interop-"
	// hostAlias is how a container reaches a service on the host. Docker Desktop
	// resolves it by itself; the explicit host-gateway entry makes the same name
	// work against a Linux daemon (see the docker run call).
	hostAlias = "host.docker.internal"

	// dockerProbeTimeout bounds a docker command that answers immediately
	// (version, image inspect, rm, ps, logs).
	dockerProbeTimeout = 20 * time.Second
	// dockerStartTimeout bounds docker run, which includes the daemon creating
	// the container.
	dockerStartTimeout = 120 * time.Second
	// dockerBuildTimeout bounds docker build, which installs pymodbus.
	dockerBuildTimeout = 300 * time.Second
	// containerReadyTimeout bounds the wait for the mapped port to accept
	// connections.
	containerReadyTimeout = 30 * time.Second
)

// requireDocker skips the test when no daemon answers, so the default suite and
// a machine without Docker never fail over it.
func requireDocker(t *testing.T) {
	t.Helper()
	out, err := runDocker(dockerProbeTimeout, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		t.Skipf("docker is unavailable, so the reference implementations are skipped: %v", err)
	}
	t.Logf("docker server %s", strings.TrimSpace(string(out)))
}

// ensureImage makes an image present, building it from dir when it is missing.
func ensureImage(t *testing.T, image, dir string) {
	t.Helper()
	if _, err := runDocker(dockerProbeTimeout, "image", "inspect", image); err == nil {
		return
	}
	t.Logf("building %s from %s (bounded to %s)", image, dir, dockerBuildTimeout)
	if _, err := runDocker(dockerBuildTimeout, "build", "-q", "-t", image, dir); err != nil {
		t.Skipf("the %s image cannot be built, so the reference implementation is unavailable: %v", image, err)
	}
}

// runDocker runs one bounded docker command and returns its combined output.
func runDocker(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// containerName is unique per process and call, so parallel runs and leftovers
// never collide.
func containerName() string {
	return fmt.Sprintf("%s%d-%08x", containerPrefix, os.Getpid(), rand.Uint32())
}

// removeContainer removes the container and checks that docker no longer lists
// it, so a leftover is reported by the suite itself.
func removeContainer(t *testing.T, name string) {
	t.Helper()
	out, err := runDocker(dockerProbeTimeout, "rm", "-f", name)
	if err != nil && !strings.Contains(string(out), "No such container") {
		t.Errorf("container %s could not be removed: %v", name, err)
	}
	out, err = runDocker(dockerProbeTimeout, "ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}")
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == name {
			t.Errorf("container %s is still listed by docker ps -a after removal", name)
		}
	}
}

// containerLogs returns the container's output, for a failure message that says
// what the reference implementation itself saw.
func containerLogs(t *testing.T, name string) string {
	t.Helper()
	out, err := runDocker(dockerProbeTimeout, "logs", name)
	if err != nil {
		return fmt.Sprintf("(docker logs %s: %v)", name, err)
	}
	return strings.TrimSpace(string(out))
}

// freePort reserves a loopback port and releases it for a mapping. The window
// between close and docker run is a few milliseconds, which is why the port is
// picked at the last moment.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if err := ln.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

// waitExchange waits until a reference implementation answers one Modbus
// exchange. A published port accepts TCP connections before the container's own
// listener is up — the docker port forwarder accepts first and closes the
// connection when the container side refuses — so a successful dial is not
// readiness and only an answer is.
func waitExchange(t *testing.T, name, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cli, err := gmodbus.NewClient(ctx, gmodbus.ClientConfig{
			Network: gmodbus.NetworkTCP,
			Addr:    addr,
			Timeout: time.Second,
		})
		if err == nil {
			_, err = cli.ReadHoldingRegisters(ctx, 1, 0, 1)
			_ = cli.Close()
		}
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Skipf("the reference implementation at %s did not answer within %s: %v\n%s",
		addr, timeout, lastErr, containerLogs(t, name))
}
