package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func FuzzP8DiagnosticEncodingIsClosedAndBounded(f *testing.F) {
	for _, seed := range [][]byte{{}, {0}, {1}, []byte("credential"), {0, 0xff, '\n', '{', '}'}} {
		f.Add(seed)
	}
	module := f.TempDir()
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 64 {
			input = input[:64]
		}
		canary := "P8_DIAGNOSTIC_" + hex.EncodeToString(input) + "_SECRET"
		dsn := "postgresql://" + canary + ":" + canary + "@127.0.0.1:1/" + canary + "?connect_timeout=1"
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), module,
			[]string{"doctor", "--provider", "postgresql", "--dsn", dsn, "--json"},
			&stdout, &stderr,
		)
		if code != 1 {
			t.Fatalf("doctor code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		if len(combined) > 4096 {
			t.Fatalf("diagnostic output is unbounded: %d bytes", len(combined))
		}
		for _, forbidden := range []string{canary, dsn, "127.0.0.1", "connect_timeout", "connection refused"} {
			if strings.Contains(combined, forbidden) {
				t.Fatalf("diagnostic output disclosed %q", forbidden)
			}
		}
		var output doctorOutput
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("closed diagnostic JSON: %v body=%q", err, stdout.String())
		}
		if output.FormatVersion != diagnosticsFormatVersion || output.Provider != "postgresql" ||
			output.Capabilities != "fail" || output.Schema != "unreachable" || len(output.Diagnostics) != 1 ||
			output.Diagnostics[0] != (doctorDiagnostic{Code: "GOLEM_DOCTOR_MODULE_INVALID", Severity: "error"}) {
			t.Fatalf("unexpected closed diagnostic shape=%#v", output)
		}
	})
}
