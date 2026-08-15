package oracle

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

const (
	PostgreSQLDSNEnv           = "GOLEM_TEST_POSTGRES_DSN"
	PostgreSQLLinguisticDSNEnv = "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"
)

// PostgreSQLProfile names one mandatory live P2 completion profile.
type PostgreSQLProfile struct {
	Name        string
	Environment string
	DSN         string
	Linguistic  bool
}

// PostgreSQLProfiles resolves both explicit live-test gates. Missing values
// are returned as an error: release callers must not silently turn this into a
// skip. A normal local test may inspect the error and choose to skip itself.
func PostgreSQLProfiles() ([]PostgreSQLProfile, error) {
	return PostgreSQLProfilesFrom(os.LookupEnv)
}

func PostgreSQLProfilesFrom(lookup func(string) (string, bool)) ([]PostgreSQLProfile, error) {
	if lookup == nil {
		return nil, fmt.Errorf("policy oracle: PostgreSQL environment lookup is nil")
	}
	specs := []PostgreSQLProfile{
		{Name: "c-default", Environment: PostgreSQLDSNEnv},
		{Name: "linguistic-default", Environment: PostgreSQLLinguisticDSNEnv, Linguistic: true},
	}
	missing := make([]string, 0, len(specs))
	for index := range specs {
		value, ok := lookup(specs[index].Environment)
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			missing = append(missing, specs[index].Environment)
			continue
		}
		specs[index].DSN = value
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("policy oracle: missing live PostgreSQL gates: %s", strings.Join(missing, ", "))
	}
	return specs, nil
}

// Expected evaluates a probe over every row of its root model and returns the
// complete true/false partition. Evaluation errors are outcomes and fail the
// agreement run; they are never interpreted as false.
func Expected(corpus Corpus, probe Probe) (SQLResult, error) {
	rows := corpus.rowsFor(probe.condition.ModelID())
	result := SQLResult{SelectionStatements: 1}
	for _, row := range rows {
		matched, err := evaluate.Condition(probe.condition, row.record, ir.PortableProviders())
		if err != nil {
			return SQLResult{}, fmt.Errorf("policy oracle: probe %q row %q: %w", probe.name, row.identity, err)
		}
		if matched {
			result.Selected = append(result.Selected, row.identity)
		} else {
			result.IsNotTrue = append(result.IsNotTrue, row.identity)
			result.Negated = append(result.Negated, row.identity)
		}
	}
	result.Selected = sortedIdentities(result.Selected)
	result.IsNotTrue = sortedIdentities(result.IsNotTrue)
	result.Negated = sortedIdentities(result.Negated)
	return result, nil
}

// VerifySQLResult checks identities, unknown count, polarity, duplicate rows,
// and the no-client-post-filtering statement-count witness.
func VerifySQLResult(corpus Corpus, probe Probe, actual SQLResult) error {
	expected, err := Expected(corpus, probe)
	if err != nil {
		return err
	}
	if actual.SelectionStatements != 1 {
		return fmt.Errorf("policy oracle: probe %q executed %d selection statements, want 1", probe.name, actual.SelectionStatements)
	}
	if actual.UnknownCount != 0 {
		return fmt.Errorf("policy oracle: probe %q returned %d SQL-unknown rows, want 0", probe.name, actual.UnknownCount)
	}
	if err := requireIdentitySet("selected", probe, actual.Selected, expected.Selected); err != nil {
		return err
	}
	if err := requireIdentitySet("IS NOT TRUE", probe, actual.IsNotTrue, expected.IsNotTrue); err != nil {
		return err
	}
	if err := requireIdentitySet("NOT(F)", probe, actual.Negated, expected.Negated); err != nil {
		return err
	}
	if !reflect.DeepEqual(sortedIdentities(actual.IsNotTrue), sortedIdentities(actual.Negated)) {
		return fmt.Errorf("policy oracle: probe %q has different IS NOT TRUE and NOT(F) identity sets", probe.name)
	}
	return nil
}

func requireIdentitySet(label string, probe Probe, actual, expected []Identity) error {
	actual = sortedIdentities(actual)
	for index := 1; index < len(actual); index++ {
		if actual[index] == actual[index-1] {
			return fmt.Errorf("policy oracle: probe %q %s contains duplicate identity %q", probe.name, label, actual[index])
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("policy oracle: probe %q %s identities = %v, want %v", probe.name, label, actual, expected)
	}
	return nil
}

func verifyControl(engine string, result ControlResult) error {
	if result.UnknownCount == 0 {
		return fmt.Errorf("policy oracle: engine %q three-valued control returned zero unknown rows", engine)
	}
	if reflect.DeepEqual(sortedIdentities(result.IsNotTrue), sortedIdentities(result.Negated)) {
		return fmt.Errorf("policy oracle: engine %q three-valued control has identical polarity sets", engine)
	}
	return nil
}

// Run executes the entire corpus through one SQL provider adapter.
func Run(ctx context.Context, corpus Corpus, engine Engine) error {
	if engine == nil {
		return fmt.Errorf("policy oracle: nil engine")
	}
	if err := ValidateCorpus(corpus); err != nil {
		return err
	}
	control, err := engine.Control(ctx, corpus)
	if err != nil {
		return fmt.Errorf("policy oracle: engine %q control: %w", engine.Name(), err)
	}
	if err := verifyControl(engine.Name(), control); err != nil {
		return err
	}
	for _, probe := range corpus.probes {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := engine.Run(ctx, corpus, probe)
		if err != nil {
			return fmt.Errorf("policy oracle: engine %q probe %q: %w", engine.Name(), probe.name, err)
		}
		if err := VerifySQLResult(corpus, probe, result); err != nil {
			return fmt.Errorf("policy oracle: engine %q: %w", engine.Name(), err)
		}
	}
	return nil
}

// ValidateCorpus proves the checked-in primary-probe/operator bijection and
// rejects ambiguous identities or mutation witnesses before provider work.
func ValidateCorpus(corpus Corpus) error {
	if corpus.seed == 0 {
		return fmt.Errorf("policy oracle: corpus seed is zero")
	}
	models := make(map[ir.ModelID]ModelSpec, len(corpus.models))
	modelNames := make(map[string]struct{}, len(corpus.models))
	for _, model := range corpus.models {
		if model.id == (ir.ModelID{}) || strings.TrimSpace(model.name) == "" || strings.TrimSpace(model.table) == "" || len(model.identityFields) == 0 {
			return fmt.Errorf("policy oracle: invalid model specification %q", model.name)
		}
		if _, duplicate := models[model.id]; duplicate {
			return fmt.Errorf("policy oracle: duplicate model identity")
		}
		if _, duplicate := modelNames[model.name]; duplicate {
			return fmt.Errorf("policy oracle: duplicate model name %q", model.name)
		}
		models[model.id], modelNames[model.name] = model, struct{}{}
	}
	fields := make(map[ir.FieldID]FieldSpec, len(corpus.fields))
	for _, field := range corpus.fields {
		if field.id == (ir.FieldID{}) || field.model == (ir.ModelID{}) || strings.TrimSpace(field.name) == "" || strings.TrimSpace(field.column) == "" {
			return fmt.Errorf("policy oracle: invalid field specification %q", field.name)
		}
		if _, ok := models[field.model]; !ok {
			return fmt.Errorf("policy oracle: field %q has unknown model", field.name)
		}
		if err := field.typeRef.Validate(); err != nil {
			return fmt.Errorf("policy oracle: field %q: %w", field.name, err)
		}
		if field.nullable != field.typeRef.Nullable() {
			return fmt.Errorf("policy oracle: field %q nullability disagrees with type", field.name)
		}
		if _, duplicate := fields[field.id]; duplicate {
			return fmt.Errorf("policy oracle: duplicate field identity")
		}
		fields[field.id] = field
	}
	for _, model := range corpus.models {
		for _, identityField := range model.identityFields {
			field, ok := fields[identityField]
			if !ok || field.model != model.id {
				return fmt.Errorf("policy oracle: model %q has an unknown or cross-model identity field", model.name)
			}
		}
	}
	relations := make(map[ir.RelationID]struct{}, len(corpus.relations))
	for _, relation := range corpus.relations {
		if relation.id == (ir.RelationID{}) || relation.field == (ir.FieldID{}) || len(relation.correlation) == 0 {
			return fmt.Errorf("policy oracle: invalid relation specification")
		}
		if _, ok := models[relation.model]; !ok {
			return fmt.Errorf("policy oracle: relation has unknown source model")
		}
		if _, ok := models[relation.target]; !ok {
			return fmt.Errorf("policy oracle: relation has unknown target model")
		}
		if relation.cardinality != ir.RelationToOne && relation.cardinality != ir.RelationToMany {
			return fmt.Errorf("policy oracle: relation has invalid cardinality")
		}
		if _, duplicate := relations[relation.id]; duplicate {
			return fmt.Errorf("policy oracle: duplicate relation identity")
		}
		for _, pair := range relation.correlation {
			parent, parentOK := fields[pair.parent]
			child, childOK := fields[pair.child]
			if !parentOK || parent.model != relation.model || !childOK || child.model != relation.target {
				return fmt.Errorf("policy oracle: relation correlation has an unknown or cross-model field")
			}
		}
		relations[relation.id] = struct{}{}
	}
	rowIDs := make(map[Identity]struct{}, len(corpus.rows))
	for _, row := range corpus.rows {
		if _, ok := models[row.model]; !ok || row.identity == "" || row.record.ModelID() != row.model || row.scope < SeedNormal || row.scope > SeedDanglingRelation {
			return fmt.Errorf("policy oracle: invalid row %q", row.identity)
		}
		if _, duplicate := rowIDs[row.identity]; duplicate {
			return fmt.Errorf("policy oracle: duplicate stable row identity %q", row.identity)
		}
		rowFields := make(map[ir.FieldID]struct{}, len(row.cells))
		for _, cell := range row.cells {
			field, ok := fields[cell.field]
			if !ok || field.model != row.model {
				return fmt.Errorf("policy oracle: row %q contains unknown or cross-model seed field", row.identity)
			}
			if _, duplicate := rowFields[cell.field]; duplicate {
				return fmt.Errorf("policy oracle: row %q contains duplicate seed field", row.identity)
			}
			rowFields[cell.field] = struct{}{}
			switch cell.kind {
			case CellNull:
				if !field.nullable || cell.value.Kind() != 0 || cell.rawJSON != "" {
					return fmt.Errorf("policy oracle: row %q has populated null seed cell", row.identity)
				}
			case CellValue:
				if err := cell.value.Validate(); err != nil || cell.rawJSON != "" || cell.value.Kind() != field.typeRef.Kind() {
					return fmt.Errorf("policy oracle: row %q has invalid value seed cell", row.identity)
				}
			case CellRawJSON:
				if strings.TrimSpace(cell.rawJSON) == "" || cell.value.Kind() != 0 || field.typeRef.Kind() != ir.ValueJSON && field.typeRef.Kind() != ir.ValueScalarList {
					return fmt.Errorf("policy oracle: row %q has invalid raw JSON seed cell", row.identity)
				}
			default:
				return fmt.Errorf("policy oracle: row %q has unknown seed cell kind %d", row.identity, cell.kind)
			}
		}
		for _, identityField := range models[row.model].identityFields {
			if _, ok := rowFields[identityField]; !ok {
				return fmt.Errorf("policy oracle: row %q omits an identity field", row.identity)
			}
		}
		rowIDs[row.identity] = struct{}{}
	}
	registry := operator.Entries()
	want := make(map[ir.OperatorID]string, len(registry))
	for _, entry := range registry {
		want[entry.ID()] = entry.Name()
	}
	seen := make(map[ir.OperatorID]string, len(want))
	names := make(map[string]struct{}, len(corpus.probes))
	mutations := make(map[string]struct{}, len(want))
	for _, probe := range corpus.probes {
		if strings.TrimSpace(probe.name) == "" || probe.condition.ModelID() == (ir.ModelID{}) {
			return fmt.Errorf("policy oracle: invalid unnamed/zero-model probe")
		}
		if _, duplicate := names[probe.name]; duplicate {
			return fmt.Errorf("policy oracle: duplicate probe name %q", probe.name)
		}
		names[probe.name] = struct{}{}
		if _, ok := want[probe.operatorID]; !ok {
			return fmt.Errorf("policy oracle: probe %q uses unknown operator %d", probe.name, probe.operatorID)
		}
		if probe.primary {
			if earlier, duplicate := seen[probe.operatorID]; duplicate {
				return fmt.Errorf("policy oracle: operator %d has primary probes %q and %q", probe.operatorID, earlier, probe.name)
			}
			if strings.TrimSpace(probe.mutation) == "" {
				return fmt.Errorf("policy oracle: primary probe %q has no mutation witness", probe.name)
			}
			if _, duplicate := mutations[probe.mutation]; duplicate {
				return fmt.Errorf("policy oracle: duplicate mutation witness %q", probe.mutation)
			}
			conditionOperator, ok := probe.condition.Operator()
			if !ok || conditionOperator != probe.operatorID {
				return fmt.Errorf("policy oracle: primary probe %q condition/operator mismatch", probe.name)
			}
			seen[probe.operatorID], mutations[probe.mutation] = probe.name, struct{}{}
		}
		if _, err := Expected(corpus, probe); err != nil {
			return err
		}
	}
	missing := make([]string, 0)
	for id, name := range want {
		if _, ok := seen[id]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 || len(seen) != len(want) {
		return fmt.Errorf("policy oracle: primary operator/probe inventory mismatch; missing=%v registry=%d probes=%d", missing, len(want), len(seen))
	}
	return nil
}
