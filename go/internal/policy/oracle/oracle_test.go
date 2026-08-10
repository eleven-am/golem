package oracle

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func TestSocialCorpusHasCanonicalShapeAndOperatorBijection(t *testing.T) {
	corpus := SocialCorpus()
	if err := ValidateCorpus(corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Seed() != CanonicalSeed {
		t.Fatalf("seed = %d, want %d", corpus.Seed(), CanonicalSeed)
	}
	models := corpus.Models()
	wantModels := []string{"User", "Post", "Comment", "Friendship", "Tag", "PostTag", "ScalarMatrix", "ScalarListMatrix", "JSONMatrix"}
	gotModels := make([]string, len(models))
	for index, model := range models {
		gotModels[index] = model.Name()
	}
	if !reflect.DeepEqual(gotModels, wantModels) {
		t.Fatalf("models = %v, want %v", gotModels, wantModels)
	}

	primary := make(map[ir.OperatorID]string)
	negated := make(map[ir.OperatorID]bool)
	for _, probe := range corpus.Probes() {
		if probe.Primary() {
			primary[probe.OperatorID()] = probe.Name()
		} else if strings.HasPrefix(probe.Name(), "not/") {
			negated[probe.OperatorID()] = true
		}
		expected, err := Expected(corpus, probe)
		if err != nil {
			t.Fatalf("%s: %v", probe.Name(), err)
		}
		rootRows := 0
		for _, row := range corpus.Rows() {
			if row.ModelID() == probe.Condition().ModelID() {
				rootRows++
			}
		}
		if len(expected.Selected)+len(expected.IsNotTrue) != rootRows || !reflect.DeepEqual(expected.IsNotTrue, expected.Negated) {
			t.Fatalf("%s did not produce a complete evaluator partition", probe.Name())
		}
	}
	entries := operator.Entries()
	if len(primary) != len(entries) || len(negated) != len(entries) {
		t.Fatalf("primary/negated/registry inventory = %d/%d/%d", len(primary), len(negated), len(entries))
	}
	for _, entry := range entries {
		if primary[entry.ID()] == "" || !negated[entry.ID()] {
			t.Fatalf("operator %d (%s) is missing its primary or negated probe", entry.ID(), entry.Name())
		}
	}
}

func TestSocialCorpusCarriesCompositeRecursiveAndAdversarialSeeds(t *testing.T) {
	corpus := SocialCorpus()
	models := make(map[string]ModelSpec)
	for _, model := range corpus.Models() {
		models[model.Name()] = model
	}
	if len(models["Friendship"].IdentityFields()) != 2 || len(models["PostTag"].IdentityFields()) != 2 {
		t.Fatal("composite identity models were not retained")
	}
	recursive := false
	for _, relation := range corpus.Relations() {
		if relation.ModelID() == models["Comment"].ID() && relation.TargetModelID() == models["Comment"].ID() && relation.Cardinality() == ir.RelationToMany {
			recursive = true
		}
	}
	if !recursive {
		t.Fatal("recursive comment relation is missing")
	}
	rawJSON := false
	dangling := false
	for _, row := range corpus.Rows() {
		if row.Scope() == SeedDanglingRelation {
			dangling = true
		}
		for _, cell := range row.Cells() {
			if raw, ok := cell.RawJSON(); ok && raw == `["go",null,7]` {
				rawJSON = true
			}
		}
	}
	if !rawJSON {
		t.Fatal("malformed-but-insertable list seed is missing")
	}
	if !dangling {
		t.Fatal("isolated dangling-relation seed is missing")
	}
}

func TestScalarMatrixCoversEveryAcceptedKindOperatorAndNullabilityCell(t *testing.T) {
	matrix := scalarMatrixFixture()
	if len(matrix.probes) != 249 {
		t.Fatalf("scalar matrix probes=%d, want 248 accepted cells plus M24", len(matrix.probes))
	}
	fields := make(map[ir.FieldID]FieldSpec, len(matrix.fields))
	for _, field := range matrix.fields {
		fields[field.ID()] = field
	}
	covered := make(map[string]bool)
	for _, probe := range matrix.probes {
		fieldID, ok := probe.Condition().Field()
		if !ok {
			continue
		}
		field := fields[fieldID]
		mode, _ := probe.Condition().Mode()
		covered[matrixCoverageKey(field.Type().Kind(), field.Nullable(), probe.OperatorID(), mode)] = true
	}
	for _, field := range matrix.fields {
		for _, entry := range operator.Entries() {
			if entry.NodeKind() != ir.ConditionScalar || !entry.AcceptsFieldKind(field.Type().Kind()) {
				continue
			}
			if entry.Nullability() == operator.NullabilityNullable && !field.Nullable() {
				continue
			}
			key := matrixCoverageKey(field.Type().Kind(), field.Nullable(), entry.ID(), ir.ComparisonSensitive)
			if !covered[key] {
				t.Errorf("missing sensitive scalar matrix cell kind=%d nullable=%v operator=%s", field.Type().Kind(), field.Nullable(), entry.Name())
			}
			if field.Type().Kind() == ir.ValueString && entry.AcceptsMode(ir.ComparisonASCIIInsensitive) {
				key = matrixCoverageKey(field.Type().Kind(), field.Nullable(), entry.ID(), ir.ComparisonASCIIInsensitive)
				if !covered[key] {
					t.Errorf("missing ASCII-insensitive scalar matrix cell nullable=%v operator=%s", field.Nullable(), entry.Name())
				}
			}
		}
	}
	if len(matrix.rows) != 3 {
		t.Fatalf("scalar matrix rows=%d, want low/high/null", len(matrix.rows))
	}
	foundM24 := false
	for _, probe := range matrix.probes {
		if probe.Name() != "scalar/astral-private-use-order" {
			continue
		}
		foundM24 = true
		expected, err := Expected(SocialCorpus(), probe)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(expected.Selected, []Identity{"scalar:low", "scalar:null"}) {
			t.Fatalf("M24 selected=%v, want private-use rows before astral", expected.Selected)
		}
	}
	if !foundM24 {
		t.Fatal("M24 astral/private-use ordering probe is missing")
	}
}

func TestListAndJSONMatricesCloseAcceptedCells(t *testing.T) {
	listMatrix := scalarListMatrixFixture()
	if len(listMatrix.fields) != 26 || len(listMatrix.probes) != 156 {
		t.Fatalf("list matrix fields/probes=%d/%d, want 26/156", len(listMatrix.fields), len(listMatrix.probes))
	}
	listCovered := make(map[string]bool, len(listMatrix.probes))
	for _, probe := range listMatrix.probes {
		fieldID, _ := probe.Condition().Field()
		listCovered[fmt.Sprintf("%x/%d", fieldID, probe.OperatorID())] = true
	}
	for _, field := range listMatrix.fields {
		element, _ := field.Type().Element()
		for _, entry := range operator.Entries() {
			if entry.NodeKind() != ir.ConditionList || entry.Nullability() == operator.NullabilityNullable && !field.Nullable() {
				continue
			}
			if entry.ID() != ir.OperatorListIsNull && entry.ID() != ir.OperatorListIsNotNull && !entry.AcceptsElementKind(element.Kind()) {
				continue
			}
			if !listCovered[fmt.Sprintf("%x/%d", field.ID(), entry.ID())] {
				t.Errorf("missing list cell element=%d nullable=%v operator=%s", element.Kind(), field.Nullable(), entry.Name())
			}
		}
	}

	jsonMatrix := jsonMatrixFixture()
	if len(jsonMatrix.fields) != 2 || len(jsonMatrix.probes) != 102 {
		t.Fatalf("JSON matrix fields/probes=%d/%d, want 2/102", len(jsonMatrix.fields), len(jsonMatrix.probes))
	}
	names := make(map[string]bool, len(jsonMatrix.probes))
	for _, probe := range jsonMatrix.probes {
		if names[probe.Name()] {
			t.Fatalf("duplicate JSON matrix probe %q", probe.Name())
		}
		names[probe.Name()] = true
	}
	for _, nullability := range []string{"required", "nullable"} {
		for _, operatorName := range []string{"json.equal", "json.not_equal"} {
			for _, operand := range []string{"null", "bool", "number", "string", "array", "object", "db-null", "document-null", "any-null"} {
				name := fmt.Sprintf("json-matrix/%s/%s/%s", nullability, operatorName, operand)
				if !names[name] {
					t.Errorf("missing JSON equality/sentinel cell %q", name)
				}
			}
		}
		for _, operatorName := range []string{"json.less_than", "json.less_than_or_equal", "json.greater_than", "json.greater_than_or_equal"} {
			for _, operand := range []string{"number", "string"} {
				if !names[fmt.Sprintf("json-matrix/%s/%s/%s", nullability, operatorName, operand)] {
					t.Errorf("missing JSON ordering cell %s/%s/%s", nullability, operatorName, operand)
				}
			}
		}
		for _, operatorName := range []string{"json.array_contains", "json.array_starts_with", "json.array_ends_with"} {
			for _, operand := range []string{"null", "bool", "number", "string", "array", "object"} {
				if !names[fmt.Sprintf("json-matrix/%s/%s/%s", nullability, operatorName, operand)] {
					t.Errorf("missing JSON array cell %s/%s/%s", nullability, operatorName, operand)
				}
			}
		}
		for _, operatorName := range []string{"json.string_contains", "json.string_starts_with", "json.string_ends_with"} {
			for _, mode := range []ir.ComparisonMode{ir.ComparisonSensitive, ir.ComparisonASCIIInsensitive} {
				name := fmt.Sprintf("json-matrix/%s/%s/string/mode-%d", nullability, operatorName, mode)
				if !names[name] {
					t.Errorf("missing JSON string-mode cell %q", name)
				}
			}
		}
	}
	for _, operatorName := range []string{"json.is_null", "json.is_not_null"} {
		if !names[fmt.Sprintf("json-matrix/nullable/%s", operatorName)] {
			t.Errorf("missing nullable JSON presence cell %s", operatorName)
		}
	}
	if len(SocialCorpus().Probes()) != 630 {
		t.Fatalf("complete oracle probes=%d, want 630", len(SocialCorpus().Probes()))
	}
}

func matrixCoverageKey(kind ir.ValueKind, nullable bool, operatorID ir.OperatorID, mode ir.ComparisonMode) string {
	return fmt.Sprintf("%d/%t/%d/%d", kind, nullable, operatorID, mode)
}

func TestRunEnforcesAgreementAndThreeValuedControl(t *testing.T) {
	corpus := SocialCorpus()
	engine := evaluatorEchoEngine{name: "echo"}
	if err := Run(context.Background(), corpus, engine); err != nil {
		t.Fatal(err)
	}

	engine.control = ControlResult{IsNotTrue: []Identity{"user:2"}}
	err := Run(context.Background(), corpus, engine)
	if err == nil || !strings.Contains(err.Error(), "zero unknown") {
		t.Fatalf("zero-unknown control error = %v", err)
	}

	engine = evaluatorEchoEngine{name: "echo", control: ControlResult{UnknownCount: 1, IsNotTrue: []Identity{"user:2"}, Negated: []Identity{"user:2"}}}
	err = Run(context.Background(), corpus, engine)
	if err == nil || !strings.Contains(err.Error(), "identical polarity") {
		t.Fatalf("dead polarity control error = %v", err)
	}
}

func TestVerifySQLResultRejectsEveryProtocolMutation(t *testing.T) {
	corpus := SocialCorpus()
	probe := corpus.Probes()[0]
	good, err := Expected(corpus, probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySQLResult(corpus, probe, good); err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(SQLResult) SQLResult{
		"extra selection statement": func(value SQLResult) SQLResult { value.SelectionStatements = 2; return value },
		"unknown row":               func(value SQLResult) SQLResult { value.UnknownCount = 1; return value },
		"wrong selected identity": func(value SQLResult) SQLResult {
			value.Selected = append(value.Selected, Identity("foreign:row"))
			return value
		},
		"duplicate selected identity": func(value SQLResult) SQLResult {
			if len(value.Selected) == 0 {
				value.Selected = []Identity{"duplicate", "duplicate"}
			} else {
				value.Selected = append(value.Selected, value.Selected[0])
			}
			return value
		},
		"wrong is-not-true polarity": func(value SQLResult) SQLResult { value.IsNotTrue = nil; return value },
		"wrong not polarity":         func(value SQLResult) SQLResult { value.Negated = nil; return value },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if err := VerifySQLResult(corpus, probe, mutate(good)); err == nil {
				t.Fatal("protocol mutation was not detected")
			}
		})
	}
}

func TestRunPropagatesAdapterAndContextErrors(t *testing.T) {
	corpus := SocialCorpus()
	want := errors.New("provider failed")
	engine := evaluatorEchoEngine{name: "broken", err: want}
	if err := Run(context.Background(), corpus, engine); !errors.Is(err, want) {
		t.Fatalf("adapter error = %v, want %v", err, want)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine = evaluatorEchoEngine{name: "cancelled"}
	if err := Run(ctx, corpus, engine); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v, want cancellation", err)
	}
}

func TestPostgreSQLProfilesRequireBothExplicitGates(t *testing.T) {
	values := map[string]string{
		PostgreSQLDSNEnv:           " postgres://c ",
		PostgreSQLLinguisticDSNEnv: " postgres://linguistic ",
	}
	profiles, err := PostgreSQLProfilesFrom(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].DSN != "postgres://c" || profiles[0].Linguistic || profiles[1].DSN != "postgres://linguistic" || !profiles[1].Linguistic {
		t.Fatalf("profiles = %#v", profiles)
	}

	delete(values, PostgreSQLLinguisticDSNEnv)
	_, err = PostgreSQLProfilesFrom(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err == nil || !strings.Contains(err.Error(), PostgreSQLLinguisticDSNEnv) {
		t.Fatalf("missing linguistic gate error = %v", err)
	}
	if _, err := PostgreSQLProfilesFrom(nil); err == nil {
		t.Fatal("nil environment lookup was accepted")
	}
}

func TestDerivedSeedsAreStableDistinctAndNonZero(t *testing.T) {
	want := []int64{CanonicalSeed, CanonicalSeed - 7046029254386353131, CanonicalSeed + 4354685564936845354}
	got := []int64{DerivedSeed(0), DerivedSeed(1), DerivedSeed(2)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derived seeds = %v, want %v", got, want)
	}
}

func TestCorpusAnswerSetsAreDiscriminating(t *testing.T) {
	corpus := SocialCorpus()
	sets := make(map[string]struct{})
	strictPrimary := 0
	primary := 0
	for _, probe := range corpus.Probes() {
		expected, err := Expected(corpus, probe)
		if err != nil {
			t.Fatal(err)
		}
		key := strings.Join(identityStrings(expected.Selected), "\x00")
		sets[key] = struct{}{}
		if probe.Primary() {
			primary++
			rootRows := 0
			for _, row := range corpus.Rows() {
				if row.ModelID() == probe.Condition().ModelID() {
					rootRows++
				}
			}
			if len(expected.Selected) > 0 && len(expected.Selected) < rootRows {
				strictPrimary++
			}
		}
	}
	if len(sets) < 18 {
		t.Fatalf("answer-set diversity=%d, want at least 18", len(sets))
	}
	if strictPrimary < 35 || primary != len(operator.Entries()) {
		t.Fatalf("strict primary answer sets=%d/%d, want at least 35/%d", strictPrimary, primary, len(operator.Entries()))
	}
}

func identityStrings(values []Identity) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = string(values[index])
	}
	return result
}

func TestCorpusAccessorsDoNotExposeSliceMutation(t *testing.T) {
	corpus := SocialCorpus()
	models := corpus.Models()
	models[0].identityFields[0] = ir.FieldID{}
	rows := corpus.Rows()
	rows[0].cells = nil
	relations := corpus.Relations()
	relations[0].correlation = nil
	probes := corpus.Probes()
	probes[0].name = "mutated"
	if err := ValidateCorpus(corpus); err != nil {
		t.Fatalf("accessor mutation leaked into corpus: %v", err)
	}
}

type evaluatorEchoEngine struct {
	name    string
	control ControlResult
	err     error
}

func (engine evaluatorEchoEngine) Name() string { return engine.name }

func (engine evaluatorEchoEngine) Control(context.Context, Corpus) (ControlResult, error) {
	if engine.err != nil {
		return ControlResult{}, engine.err
	}
	if engine.control.UnknownCount == 0 && len(engine.control.IsNotTrue) == 0 && len(engine.control.Negated) == 0 {
		return ControlResult{UnknownCount: 1, IsNotTrue: []Identity{"user:2"}}, nil
	}
	return engine.control, nil
}

func (engine evaluatorEchoEngine) Run(_ context.Context, corpus Corpus, probe Probe) (SQLResult, error) {
	if engine.err != nil {
		return SQLResult{}, engine.err
	}
	return Expected(corpus, probe)
}
