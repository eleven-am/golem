package explain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
)

func TestMigrationExplainReportIsImmutableAndRenderersShareOneReport(t *testing.T) {
	input := reportInput{
		formatVersion: reportFormatVersion,
		mode:          modeProspective,
		providers: []providerInput{{
			provider:          ir.PostgreSQL,
			initial:           false,
			beforeFingerprint: digest('a'),
			afterFingerprint:  digest('b'),
			artifacts: []artifactInput{{
				path:   "postgresql/002.sql",
				sha256: digest('c'),
			}},
			phases: []phaseInput{{
				ordinal:           0,
				mode:              migration.Transactional,
				beforeFingerprint: digest('a'),
				afterFingerprint:  digest('b'),
				operations: []operationInput{{
					id:               operationID('2'),
					kind:             migration.BackfillColumn,
					stage:            45,
					identity:         identityInput{modelID: ir.ModelID(stableID('3')), fieldID: ir.FieldID(stableID('4'))},
					display:          displayInput{model: "Post", member: "Slug"},
					risk:             migration.RiskManual,
					mode:             migration.Transactional,
					before:           digest('d'),
					after:            digest('e'),
					dependencies:     []migration.OperationID{operationID('1')},
					capabilities:     []ir.CapabilityID{"capability-a"},
					approvalRequired: true,
					approvalPresent:  true,
					effect:           effectInput{beforePresent: true, afterPresent: true},
					manual: &manualInput{
						path:          "postgresql/002.backfill.sql",
						sha256:        digest('f'),
						postcondition: digest('1'),
					},
				}},
			}},
		}},
	}
	report, err := buildReport(input)
	if err != nil {
		t.Fatal(err)
	}

	providers := report.Providers()
	providers[0] = Provider{}
	if got := report.Providers()[0].Provider(); got != ir.PostgreSQL {
		t.Fatalf("report provider mutated through accessor: %q", got)
	}
	operations := report.Providers()[0].Phases()[0].Operations()
	dependencies := operations[0].Dependencies()
	dependencies[0] = "mutated"
	if got := report.Providers()[0].Phases()[0].Operations()[0].Dependencies()[0]; got != operationID('1') {
		t.Fatalf("operation dependency mutated through accessor: %q", got)
	}

	jsonBytes, err := MarshalJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	projectedBytes, err := json.Marshal(compatibilityJSONProjection(report))
	if err != nil || !bytes.Equal(jsonBytes, projectedBytes) {
		t.Fatalf("custom JSON renderer diverged from compatibility projection: err=%v\nrenderer=%s\nprojection=%s", err, jsonBytes, projectedBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(jsonBytes))
	decoder.DisallowUnknownFields()
	var decoded JSONCompatibilityDocument
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("strict-decode custom JSON through compatibility projection: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("custom JSON compatibility projection trailing input: %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(jsonBytes, reencoded) {
		t.Fatalf("strict compatibility projection did not preserve custom JSON bytes: err=%v\nrenderer=%s\nreencoded=%s", err, jsonBytes, reencoded)
	}
	textBytes, err := MarshalText(report)
	if err != nil {
		t.Fatal(err)
	}
	var machine struct {
		FormatVersion uint16 `json:"formatVersion"`
		Mode          string `json:"mode"`
		Providers     []struct {
			OperationCountsByRisk []struct {
				Risk  string `json:"risk"`
				Count uint32 `json:"count"`
			} `json:"operationCountsByRisk"`
			Phases []struct {
				Operations []struct {
					ID           string   `json:"id"`
					Risk         string   `json:"risk"`
					Effect       string   `json:"effect"`
					Dependencies []string `json:"dependencies"`
					Capabilities []string `json:"capabilities"`
					Approval     struct {
						Required bool `json:"required"`
						Present  bool `json:"present"`
					} `json:"approval"`
					ReviewedCompanion struct {
						Path                string `json:"path"`
						SHA256              string `json:"sha256"`
						PostconditionDigest string `json:"postconditionDigest"`
					} `json:"reviewedCompanion"`
					Warnings []string `json:"warnings"`
				} `json:"operations"`
			} `json:"phases"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(jsonBytes, &machine); err != nil {
		t.Fatal(err)
	}
	if machine.FormatVersion != 1 || machine.Mode != "prospective" {
		t.Fatalf("machine header=%+v", machine)
	}
	operation := machine.Providers[0].Phases[0].Operations[0]
	if operation.ID != string(operationID('2')) || operation.Risk != "manual" || operation.Effect != "manualDataTransform" || !operation.Approval.Required || !operation.Approval.Present || len(operation.Dependencies) != 1 || len(operation.Capabilities) != 1 || operation.ReviewedCompanion.Path != "postgresql/002.backfill.sql" || operation.ReviewedCompanion.PostconditionDigest != string(digest('1')) || !contains(operation.Warnings, string(WarningReviewedBackfill)) {
		t.Fatalf("machine operation=%+v", operation)
	}
	if counts := machine.Providers[0].OperationCountsByRisk; len(counts) != 5 || counts[4].Risk != "manual" || counts[4].Count != 1 {
		t.Fatalf("risk counts=%+v", counts)
	}
	for _, required := range []string{string(operationID('2')), "manual data transform", string(operationID('1')), "approval: present", "reviewed backfill"} {
		if !strings.Contains(string(textBytes), required) {
			t.Fatalf("text lacks %q:\n%s", required, textBytes)
		}
	}
}

func TestMigrationExplainEffectMappingIsExhaustive(t *testing.T) {
	all := []migration.OperationKind{
		migration.BootstrapSystemSchema, migration.AddSystemObject, migration.CreateNamespace,
		migration.CreateTable, migration.RenameTable, migration.DropTable, migration.AddColumn,
		migration.RenameColumn, migration.AlterColumnType, migration.AlterColumnNullability,
		migration.SetColumnDefault, migration.DropColumnDefault, migration.DropColumn,
		migration.AddPrimaryKey, migration.DropPrimaryKey, migration.AddUnique, migration.DropUnique,
		migration.AddForeignKey, migration.DropForeignKey, migration.AddCheck, migration.DropCheck,
		migration.CreateIndex, migration.DropIndex, migration.RenameIndex,
		migration.CreateProviderExtension, migration.DropProviderExtension, migration.BackfillColumn,
		migration.InitializeConcurrencyColumn,
		migration.RebuildTable, migration.ValidateConstraint, migration.ManualStep,
		migration.RecordSchemaVersion,
	}
	if got, want := knownOperationKinds(), all; len(got) != len(want) {
		t.Fatalf("known operation kinds=%d want=%d", len(got), len(want))
	} else {
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("operation kind[%d]=%q want=%q", index, got[index], want[index])
			}
		}
	}
	for _, kind := range all {
		effect, warnings, err := classifyEffect(kind, effectFactsForKind(kind))
		if err != nil || effect == "" {
			t.Fatalf("kind %q effect=%q warnings=%v err=%v", kind, effect, warnings, err)
		}
	}
	if effect, _, _ := classifyEffect(migration.DropColumn, effectInput{beforePresent: true}); effect != effectValueDeleted {
		t.Fatalf("drop column effect=%q", effect)
	}
	if effect, _, _ := classifyEffect(migration.AlterColumnType, effectInput{beforePresent: true, afterPresent: true, preservation: preservationSafeWidening}); effect != effectValuePreserving {
		t.Fatalf("safe widening effect=%q", effect)
	}
	if effect, _, _ := classifyEffect(migration.AlterColumnType, effectInput{beforePresent: true, afterPresent: true, preservation: preservationRewrite}); effect != effectValueRewritten {
		t.Fatalf("rewrite effect=%q", effect)
	}
	if effect, _, _ := classifyEffect(migration.BackfillColumn, effectInput{beforePresent: true, afterPresent: true}); effect != effectManualDataTransform {
		t.Fatalf("backfill effect=%q", effect)
	}
	if effect, _, _ := classifyEffect(migration.CreateProviderExtension, effectInput{afterPresent: true}); effect != effectSchemaOnly {
		t.Fatalf("new provider extension effect=%q", effect)
	}
	if effect, _, _ := classifyEffect(migration.CreateProviderExtension, effectInput{afterPresent: true, extensionRecreation: true}); effect != effectValueRewritten {
		t.Fatalf("recreated provider extension effect=%q", effect)
	}
	if effect, _, _ := classifyEffect(migration.DropProviderExtension, effectInput{beforePresent: true}); effect != effectValueDeleted {
		t.Fatalf("permanent provider extension removal effect=%q", effect)
	}
	if effect, _, _ := classifyEffect(migration.DropProviderExtension, effectInput{beforePresent: true, extensionRecreation: true}); effect != effectValueRewritten {
		t.Fatalf("provider extension recreation drop effect=%q", effect)
	}
	permanentDrop, err := buildOperation(operationInput{
		id: operationID('8'), kind: migration.DropProviderExtension,
		identity: identityInput{extensionID: ir.ExtensionID(stableID('9'))},
		risk:     migration.RiskDataLoss, mode: migration.Transactional,
		before: digest('a'), approvalRequired: true,
		effect: effectInput{beforePresent: true},
	})
	if err != nil || permanentDrop.Effect() != EffectValueDeleted || !containsWarning(permanentDrop.Warnings(), WarningDataLoss) {
		t.Fatalf("permanent extension drop=%+v warnings=%v err=%v", permanentDrop, permanentDrop.Warnings(), err)
	}
	if effect, warnings, err := classifyEffect(migration.AlterColumnType, effectInput{beforePresent: true, afterPresent: true}); err != nil || effect != EffectUnknown || !containsWarning(warnings, WarningManualReview) {
		t.Fatalf("unproved type effect=%q warnings=%v err=%v", effect, warnings, err)
	}
	if _, _, err := classifyEffect(migration.OperationKind("futureOperation"), effectInput{}); !isCode(err, codeUnavailable) {
		t.Fatalf("unknown kind error=%v", err)
	}
}

func TestMigrationExplainRejectsUnknownVersionKindAndHardBoundsWithoutPartialOutput(t *testing.T) {
	missingVersion := noChangeInput()
	missingVersion.formatVersion = 0
	if _, err := buildReport(missingVersion); !isCode(err, codeUnavailable) {
		t.Fatalf("missing version error=%v", err)
	}

	base := noChangeInput()
	base.formatVersion = 2
	if _, err := buildReport(base); !isCode(err, codeUnavailable) {
		t.Fatalf("unknown version error=%v", err)
	}

	base = noChangeInput()
	base.providers[0].phases = []phaseInput{{
		ordinal: 0, mode: migration.Transactional,
		beforeFingerprint: digest('a'), afterFingerprint: digest('a'),
		operations: []operationInput{{
			id: operationID('1'), kind: migration.OperationKind("futureOperation"),
			risk: migration.RiskSafe, mode: migration.Transactional,
		}},
	}}
	if _, err := buildReport(base); !isCode(err, codeUnavailable) {
		t.Fatalf("unknown kind error=%v", err)
	}

	base = noChangeInput()
	base.providers[0].artifacts = []artifactInput{{path: strings.Repeat("x", maxStringBytes+1), sha256: digest('c')}}
	if _, err := buildReport(base); !isCode(err, codeUnavailable) {
		t.Fatalf("oversized label error=%v", err)
	}

	base = noChangeInput()
	base.providers = append(base.providers, base.providers[0], base.providers[0])
	if _, err := buildReport(base); !isCode(err, codeUnavailable) {
		t.Fatalf("provider overflow error=%v", err)
	}
}

func TestMigrationExplainRenderersAreDeterministicBoundedAndPrivate(t *testing.T) {
	input := noChangeInput()
	report, err := buildReport(input)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := MarshalJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, _ := MarshalJSON(report)
	firstText, err := MarshalText(report)
	if err != nil {
		t.Fatal(err)
	}
	secondText, _ := MarshalText(report)
	if string(firstJSON) != string(secondJSON) || string(firstText) != string(secondText) {
		t.Fatal("rendering is nondeterministic")
	}
	if len(firstJSON) > maxEncodedBytes || len(firstText) > maxEncodedBytes {
		t.Fatalf("encoded report exceeds bound json=%d text=%d", len(firstJSON), len(firstText))
	}
	if !strings.Contains(string(firstText), "NO CHANGES") || !strings.Contains(string(firstText), "zero downtime is not guaranteed") {
		t.Fatalf("no-change guarantees missing:\n%s", firstText)
	}
	for _, forbidden := range []string{"postgresql://secret", "/Users/royossai", "CREATE TABLE", "physical_secret_table"} {
		if strings.Contains(string(firstJSON), forbidden) || strings.Contains(string(firstText), forbidden) {
			t.Fatalf("rendered private canary %q", forbidden)
		}
	}
}

func TestMigrationExplainCountsEveryClosedBoundDuringBuildAndEncoding(t *testing.T) {
	overflowDependencies := make([]migration.OperationID, maxCollectionItems+1)
	if _, err := buildOperation(operationInput{
		id: operationID('1'), kind: migration.CreateTable, risk: migration.RiskSafe,
		mode: migration.Transactional, dependencies: overflowDependencies,
		effect: effectInput{afterPresent: true},
	}); !isCode(err, codeUnavailable) {
		t.Fatalf("dependency overflow error=%v", err)
	}

	invalidUTF8 := noChangeInput()
	invalidUTF8.providers[0].artifacts = []artifactInput{{path: string([]byte{0xff}), sha256: digest('a')}}
	if _, err := buildReport(invalidUTF8); !isCode(err, codeUnavailable) {
		t.Fatalf("invalid UTF-8 error=%v", err)
	}

	for _, injected := range []string{"reviewed.sql\nStatus: NO CHANGES", "reviewed.sql\rhidden", "reviewed.sql\u2028hidden", "reviewed.sql\u2029hidden"} {
		control := noChangeInput()
		control.providers[0].artifacts = []artifactInput{{path: injected, sha256: digest('a')}}
		if _, err := buildReport(control); !isCode(err, codeUnavailable) {
			t.Fatalf("text control path %q error=%v", injected, err)
		}
	}

	largeLabel := strings.Repeat("X", maxStringBytes)
	operations := make([]operationInput, 4_200)
	for index := range operations {
		operations[index] = operationInput{
			id: operationID('1'), kind: migration.CreateTable, risk: migration.RiskSafe,
			mode: migration.Transactional, identity: identityInput{modelID: ir.ModelID(stableID('2'))},
			display: displayInput{model: largeLabel}, effect: effectInput{afterPresent: true},
		}
	}
	provider, count, err := buildProvider(providerInput{
		provider: ir.SQLite, beforeFingerprint: digest('a'), afterFingerprint: digest('b'),
		phases: []phaseInput{{
			ordinal: 0, mode: migration.Transactional, beforeFingerprint: digest('a'),
			afterFingerprint: digest('b'), operations: operations,
		}},
	})
	if err != nil || count != len(operations) {
		t.Fatalf("large provider count=%d err=%v", count, err)
	}
	report := Report{
		formatVersion: reportFormatVersion, mode: ModeProspective,
		status: StatusReviewRequired, providers: []Provider{provider},
		warnings: []Warning{WarningZeroDowntimeNotGuaranteed},
	}
	if output, err := MarshalJSON(report); output != nil || !isCode(err, codeUnavailable) {
		t.Fatalf("oversized JSON bytes=%d err=%v", len(output), err)
	}
	if output, err := MarshalText(report); output != nil || !isCode(err, codeUnavailable) {
		t.Fatalf("oversized text bytes=%d err=%v", len(output), err)
	}
	input := reportInput{formatVersion: reportFormatVersion, mode: ModeProspective, providers: []providerInput{{
		provider: ir.SQLite, beforeFingerprint: digest('a'), afterFingerprint: digest('b'),
		phases: []phaseInput{{
			ordinal: 0, mode: migration.Transactional, beforeFingerprint: digest('a'),
			afterFingerprint: digest('b'), operations: operations,
		}},
	}}}
	if _, err := buildReport(input); !isCode(err, codeUnavailable) {
		t.Fatalf("build-time encoded bound error=%v", err)
	}
}

func noChangeInput() reportInput {
	return reportInput{
		formatVersion: reportFormatVersion,
		mode:          modeReviewed,
		providers: []providerInput{{
			provider:          ir.SQLite,
			beforeFingerprint: digest('a'),
			afterFingerprint:  digest('a'),
		}},
	}
}

func digest(value byte) migration.Digest { return migration.Digest(strings.Repeat(string(value), 64)) }

func operationID(value byte) migration.OperationID {
	return migration.OperationID(strings.Repeat(string(value), 32))
}

func stableID(value byte) string { return strings.Repeat(string(value), 32) }

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsWarning(values []Warning, wanted Warning) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func effectFactsForKind(kind migration.OperationKind) effectInput {
	switch kind {
	case migration.DropTable, migration.DropColumn, migration.DropPrimaryKey, migration.DropUnique,
		migration.DropForeignKey, migration.DropCheck, migration.DropIndex, migration.DropProviderExtension:
		return effectInput{beforePresent: true}
	case migration.BootstrapSystemSchema, migration.AddSystemObject, migration.CreateNamespace,
		migration.CreateTable, migration.AddColumn, migration.AddPrimaryKey, migration.AddUnique,
		migration.AddForeignKey, migration.AddCheck, migration.CreateIndex, migration.CreateProviderExtension:
		return effectInput{afterPresent: true}
	case migration.AlterColumnType:
		return effectInput{beforePresent: true, afterPresent: true, preservation: preservationSafeWidening}
	case migration.RebuildTable:
		return effectInput{beforePresent: true, afterPresent: true, preservation: preservationRewrite}
	case migration.InitializeConcurrencyColumn:
		return effectInput{beforePresent: true, afterPresent: true, preservation: preservationRewrite}
	default:
		return effectInput{beforePresent: true, afterPresent: true}
	}
}

func TestMigrationExplainRejectsPhysicalAndSQLPresentationInputs(t *testing.T) {
	base := noChangeInput()
	base.providers[0].beforeFingerprint = digest('a')
	base.providers[0].afterFingerprint = digest('b')
	base.providers[0].phases = []phaseInput{{
		ordinal: 0, mode: migration.Transactional, beforeFingerprint: digest('a'), afterFingerprint: digest('b'),
		operations: []operationInput{{
			id: operationID('1'), kind: migration.CreateNamespace, risk: migration.RiskSafe,
			mode: migration.Transactional, effect: effectInput{afterPresent: true},
			display: displayInput{model: "physical_secret_namespace"},
		}},
	}}
	if _, err := buildReport(base); !isCode(err, codeUnavailable) {
		t.Fatalf("physical namespace presentation error=%v", err)
	}

	base.providers[0].phases[0].operations[0] = operationInput{
		id: operationID('1'), kind: migration.CreateTable, risk: migration.RiskSafe,
		mode: migration.Transactional, effect: effectInput{afterPresent: true},
		identity: identityInput{modelID: ir.ModelID(stableID('2'))},
		display:  displayInput{model: "CREATE TABLE secret"},
	}
	if _, err := buildReport(base); !isCode(err, codeUnavailable) {
		t.Fatalf("SQL presentation error=%v", err)
	}

	base.providers[0].phases[0].operations[0].display = displayInput{model: "/Users/royossai/private"}
	if _, err := buildReport(base); !isCode(err, codeUnavailable) {
		t.Fatalf("absolute presentation error=%v", err)
	}

	if code, ok := CodeOf(fmt.Errorf("outer: %w", unavailable())); !ok || code != codeUnavailable {
		t.Fatalf("wrapped code=(%q,%v)", code, ok)
	}
}
