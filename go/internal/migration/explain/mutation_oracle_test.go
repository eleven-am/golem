package explain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
)

func TestMigrationExplainMutationOrderAndEffectOracle(t *testing.T) {
	report, err := buildReport(mutationReportInput())
	if err != nil {
		t.Fatal(err)
	}
	operations := report.providers[0].phases[0].operations
	if got, want := []migration.OperationID{operations[0].id, operations[1].id}, []migration.OperationID{operationID('1'), operationID('2')}; !reflect.DeepEqual(got, want) {
		t.Fatalf("authoritative operation order=%v want=%v", got, want)
	}
	checks := []struct {
		name  string
		kind  migration.OperationKind
		facts effectInput
		want  Effect
	}{
		{"data loss", migration.DropColumn, effectInput{beforePresent: true}, EffectValueDeleted},
		{"rewrite", migration.AlterColumnType, effectInput{beforePresent: true, afterPresent: true, preservation: preservationRewrite}, EffectValueRewritten},
		{"unknown", migration.AlterColumnType, effectInput{beforePresent: true, afterPresent: true}, EffectUnknown},
	}
	for _, check := range checks {
		effect, warnings, classifyErr := classifyEffect(check.kind, check.facts)
		if classifyErr != nil || effect != check.want {
			t.Fatalf("%s effect=%q warnings=%v err=%v", check.name, effect, warnings, classifyErr)
		}
		if effect == EffectUnknown && !containsWarning(warnings, WarningManualReview) {
			t.Fatalf("unknown warnings=%v", warnings)
		}
	}
}

func TestMigrationExplainMutationFactCompletenessOracle(t *testing.T) {
	report, err := buildReport(mutationReportInput())
	if err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := MarshalJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	textBytes, err := MarshalText(report)
	if err != nil {
		t.Fatal(err)
	}
	operation := report.providers[0].phases[0].operations[1]
	manual, ok := operation.ManualCompanion()
	if !operation.ApprovalRequired() || !operation.ApprovalPresent() || !reflect.DeepEqual(operation.Dependencies(), []migration.OperationID{operationID('1')}) || !ok || manual.Path() != "postgresql/002.backfill.sql" || manual.Postcondition() != digest('7') {
		t.Fatalf("report facts approval=(%v,%v) deps=%v manual=(%+v,%v)", operation.ApprovalRequired(), operation.ApprovalPresent(), operation.Dependencies(), manual, ok)
	}
	for _, required := range []string{
		string(operationID('1')), `"required":true`, `"present":true`,
		`"reviewedCompanion"`, `"postconditionDigest":"` + string(digest('7')) + `"`,
		"depends on: " + string(operationID('1')), "approval: present",
		"reviewed backfill: postgresql/002.backfill.sql", "postcondition: " + string(digest('7')),
	} {
		if !strings.Contains(string(jsonBytes), required) && !strings.Contains(string(textBytes), required) {
			t.Fatalf("shared renderings omit %q\nJSON: %s\nTEXT: %s", required, jsonBytes, textBytes)
		}
	}
}

func TestMigrationExplainMutationGuaranteeAndClosedJSONOracle(t *testing.T) {
	report, err := buildReport(mutationReportInput())
	if err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := MarshalJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(jsonBytes, &document); err != nil {
		t.Fatal(err)
	}
	if got, want := sortedKeys(document), []string{"formatVersion", "guarantees", "mode", "providers", "status", "warnings"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level JSON keys=%v want=%v", got, want)
	}
	if document["formatVersion"] != float64(1) {
		t.Fatalf("formatVersion=%v", document["formatVersion"])
	}
	guarantees, ok := document["guarantees"].(map[string]any)
	if !ok {
		t.Fatalf("guarantees=%T", document["guarantees"])
	}
	if got, want := sortedKeys(guarantees), []string{"appliesChanges", "durationEstimated", "usesReviewedTypedPlan", "zeroDowntime"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("guarantee keys=%v want=%v", got, want)
	}
	if guarantees["appliesChanges"] != false || guarantees["usesReviewedTypedPlan"] != true || guarantees["zeroDowntime"] != false || guarantees["durationEstimated"] != false {
		t.Fatalf("guarantees=%v", guarantees)
	}
	textBytes, err := MarshalText(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(textBytes), "no duration estimate; zero downtime is not guaranteed") {
		t.Fatalf("text guarantees=%s", textBytes)
	}
}

func TestMigrationExplainMutationPrivacyAndValidationOracle(t *testing.T) {
	report, err := buildReport(mutationReportInput())
	if err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := MarshalJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	textBytes, err := MarshalText(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range migrationExplainPrivacyCanaries() {
		if strings.Contains(string(jsonBytes), canary) || strings.Contains(string(textBytes), canary) {
			t.Fatalf("private canary %q escaped", canary)
		}
	}

	malformed := report
	malformed.providers = cloneProviders(report.providers)
	malformed.providers[0].phases[0].operations[0].display = "CREATE TABLE physical_secret_table"
	if output, renderErr := MarshalJSON(malformed); output != nil || !isCode(renderErr, codeUnavailable) {
		t.Fatalf("malformed JSON bytes=%d err=%v", len(output), renderErr)
	}
	if output, renderErr := MarshalText(malformed); output != nil || !isCode(renderErr, codeUnavailable) {
		t.Fatalf("malformed text bytes=%d err=%v", len(output), renderErr)
	}
}

func TestMigrationExplainMutationReadOnlyAndProviderValidationOracle(t *testing.T) {
	workingOutput := "golem-migration-plan-mutant-output"
	_ = os.Remove(workingOutput)
	t.Cleanup(func() { _ = os.Remove(workingOutput) })
	tempPattern := filepath.Join(os.TempDir(), "golem-migration-plan-mutant-*")
	beforeTemps, err := filepath.Glob(tempPattern)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildReport(mutationReportInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workingOutput); !os.IsNotExist(err) {
		t.Fatalf("prospective explanation wrote %s: %v", workingOutput, err)
	}
	afterTemps, err := filepath.Glob(tempPattern)
	if err != nil {
		t.Fatal(err)
	}
	newTemps := stringDifference(afterTemps, beforeTemps)
	for _, name := range newTemps {
		_ = os.RemoveAll(name)
	}
	if len(newTemps) != 0 {
		t.Fatalf("prospective explanation leaked temporary paths: %v", newTemps)
	}

	invalidSecondProvider := mutationReportInput()
	invalidSecondProvider.providers = append(invalidSecondProvider.providers, providerInput{
		provider: ir.Provider("future"), beforeFingerprint: digest('a'), afterFingerprint: digest('a'),
	})
	if _, err := buildReport(invalidSecondProvider); !isCode(err, codeUnavailable) {
		t.Fatalf("invalid declared provider hidden by presentation filter: %v", err)
	}
}

func mutationReportInput() reportInput {
	return reportInput{
		formatVersion: reportFormatVersion,
		mode:          ModeReviewed,
		providers: []providerInput{{
			provider: ir.PostgreSQL, beforeFingerprint: digest('a'), afterFingerprint: digest('b'),
			phases: []phaseInput{{
				ordinal: 0, mode: migration.Transactional,
				beforeFingerprint: digest('a'), afterFingerprint: digest('b'),
				operations: []operationInput{
					{
						id: operationID('1'), kind: migration.CreateTable, stage: 20,
						identity: identityInput{modelID: ir.ModelID(stableID('3'))},
						display:  displayInput{model: "Post"}, risk: migration.RiskSafe,
						mode: migration.Transactional, after: digest('4'), effect: effectInput{afterPresent: true},
					},
					{
						id: operationID('2'), kind: migration.BackfillColumn, stage: 45,
						identity: identityInput{modelID: ir.ModelID(stableID('3')), fieldID: ir.FieldID(stableID('5'))},
						display:  displayInput{model: "Post", member: "Slug"}, risk: migration.RiskManual,
						mode: migration.Transactional, before: digest('4'), after: digest('6'),
						dependencies: []migration.OperationID{operationID('1')}, approvalRequired: true, approvalPresent: true,
						effect: effectInput{beforePresent: true, afterPresent: true},
						manual: &manualInput{path: "postgresql/002.backfill.sql", sha256: digest('8'), postcondition: digest('7')},
					},
				},
			}},
		}},
	}
}

func migrationExplainPrivacyCanaries() []string {
	return []string{
		"CREATE TABLE physical_secret_table", "bound-secret-value", "postgresql://secret@localhost/private",
		"/Users/royossai/private.sql", "physical_secret_table",
	}
}

func sortedKeys(values map[string]any) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	slicesSort(result)
	return result
}

func slicesSort(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func stringDifference(left, right []string) []string {
	known := make(map[string]bool, len(right))
	for _, value := range right {
		known[value] = true
	}
	result := make([]string, 0)
	for _, value := range left {
		if !known[value] {
			result = append(result, value)
		}
	}
	return result
}
