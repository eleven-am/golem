package main

import (
	"bytes"
	"encoding/json"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	migrationexplain "github.com/eleven-am/golem/go/internal/migration/explain"
)

func TestEveryMachineJSONDocumentCarriesExactFormatVersion(t *testing.T) {
	tests := []struct {
		name  string
		want  uint16
		value any
	}{
		{name: "build-diagnostics", want: buildDiagnosticsOutputFormatVersion, value: buildDiagnosticsOutput{FormatVersion: buildDiagnosticsOutputFormatVersion}},
		{name: "doctor", want: diagnosticsFormatVersion, value: newDoctorOutput("sqlite")},
		{name: "generate-check", want: generationOutputFormatVersion, value: generateOutput{FormatVersion: generationOutputFormatVersion, Changed: []string{}, Stale: []string{}}},
		{name: "inspect", want: 2, value: inspectOutput{FormatVersion: 2}},
		{name: "migration-apply", want: migrationOutputFormatVersion, value: migrationApplyOutput{FormatVersion: migrationOutputFormatVersion, Applied: []migration.MigrationID{}}},
		{name: "migration-new", want: migrationOutputFormatVersion, value: migrationNewOutput{FormatVersion: migrationOutputFormatVersion, Providers: []compilerir.Provider{}, Changed: []string{}}},
		{name: "migration-plan", want: 1, value: migrationexplain.JSONCompatibilitySource()},
		{name: "version", want: diagnosticsFormatVersion, value: currentVersion()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := encodeJSON(&output, test.value); err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				FormatVersion uint16 `json:"formatVersion"`
			}
			decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
			if err := decoder.Decode(&envelope); err != nil || envelope.FormatVersion != test.want {
				t.Fatalf("machine JSON format version=%d want=%d err=%v", envelope.FormatVersion, test.want, err)
			}
		})
	}
}
