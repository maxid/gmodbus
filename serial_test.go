package gmodbus

import (
	"testing"

	"go.bug.st/serial"
)

// TestSerialStopBits pins the mapping between this package's stop-bit count and
// the serial package's identifiers. The two disagree (a count of 1 is not the
// identifier 1), and a straight cast silently opened every line with two stop
// bits — or rejected TwoStopBits as invalid — so the mapping is worth a test
// that needs no hardware.
func TestSerialStopBits(t *testing.T) {
	cases := []struct {
		name string
		in   StopBits
		want serial.StopBits
	}{
		{"one", OneStopBit, serial.OneStopBit},
		{"two", TwoStopBits, serial.TwoStopBits},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serialStopBits(tc.in); got != tc.want {
				t.Fatalf("serialStopBits(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
	// The identifiers must actually differ; otherwise the mapping above would
	// pass by accident if go.bug.st/serial renumbered them.
	if serial.OneStopBit == serial.TwoStopBits {
		t.Fatal("go.bug.st/serial uses one identifier for both stop-bit counts")
	}
}

// TestSerialDefaults pins the line parameters a zero SerialConfig means: the
// 19200 8N1 the README promises, including one stop bit rather than the two the
// specification asks for on a no-parity line.
func TestSerialDefaults(t *testing.T) {
	cfg, err := normalizeSerial(nil)
	if err != nil {
		t.Fatalf("normalizeSerial(nil): %v", err)
	}
	if DefaultSerialBaud != 19200 {
		t.Fatalf("DefaultSerialBaud = %d, want 19200", DefaultSerialBaud)
	}
	if cfg.Baud != DefaultSerialBaud {
		t.Fatalf("default baud = %d, want %d", cfg.Baud, DefaultSerialBaud)
	}
	if cfg.DataBits != 8 || cfg.StopBits != OneStopBit || cfg.Parity != NoParity {
		t.Fatalf("default framing = %d data bits, %d stop bits, parity %d, want 8N1",
			cfg.DataBits, cfg.StopBits, cfg.Parity)
	}
}
