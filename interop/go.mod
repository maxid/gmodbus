// This module holds the interoperability tests against an independent Modbus
// implementation. It is deliberately separate so the library's module graph
// does not carry a third-party Modbus dependency.
module github.com/maxid/gmodbus/interop

go 1.27

require (
	github.com/creack/pty v1.1.24
	github.com/maxid/gmodbus v0.0.0
	github.com/simonvetter/modbus v1.6.4
)

require (
	github.com/goburrow/serial v0.1.0 // indirect
	github.com/maxid/gnety v0.0.0-20261101072215-3fa5d9d714c6 // indirect
	github.com/pion/dtls/v2 v2.2.12 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/transport/v2 v2.2.4 // indirect
	go.bug.st/serial v1.8.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/maxid/gmodbus => ../
