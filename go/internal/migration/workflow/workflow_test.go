package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/generate/publication"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestSQLiteProviderHistoryAllowsOnlyTheReviewedDriverTransition(t *testing.T) {
	vec := physical.CapabilityFact{ID: "sqlite.vec0.v1", Version: 1, Verification: physical.VerificationRuntimeProbe}
	current := physical.SQLiteManifest(vec)
	historical := current
	historical.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	historical.Capabilities = append([]physical.CapabilityFact(nil), historical.Capabilities[:len(historical.Capabilities)-1]...)
	if !physical.CompatibleProviderHistory(historical, current) {
		t.Fatal("reviewed modernc-to-ncruces transition was rejected")
	}
	forged := historical
	forged.Driver.Module = "example.com/unknown"
	if physical.CompatibleProviderHistory(forged, current) {
		t.Fatal("unknown historical driver was accepted")
	}
	downgraded := current
	downgraded.Capabilities = nil
	if physical.CompatibleProviderHistory(historical, downgraded) {
		t.Fatal("capability-losing provider transition was accepted")
	}
}

func TestInitialIncrementalDeterminismAndNoOpRefusal(t *testing.T) {
	compiled, providers := socialResult(t)
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
	if initial.MigrationID != "0001_initial" || len(initial.Providers) != 2 {
		t.Fatalf("initial result = %#v", initial)
	}
	publish(t, module, initial)
	assertHistoryArtifacts(t, module, "0001_initial", 2)

	previous := compiled.Compilation.Model
	current := cloneModel(t, previous)
	current.Schema.StableName += "_v2"
	currentFingerprint, _ := ir.ModelFingerprint(current)
	updatedProviders := addPortableIndex(t, providers)
	incremental := prepareWithFingerprint(t, module, "add_lookup", current, &previous, currentFingerprint, compiled.ContractFingerprint, updatedProviders, nil)
	reversedProviders := append([]Provider(nil), updatedProviders...)
	for left, right := 0, len(reversedProviders)-1; left < right; left, right = left+1, right-1 {
		reversedProviders[left], reversedProviders[right] = reversedProviders[right], reversedProviders[left]
	}
	repeated := prepareWithFingerprint(t, module, "add_lookup", current, &previous, currentFingerprint, compiled.ContractFingerprint, reversedProviders, nil)
	if !bytes.Equal(incremental.Prospective.Bytes, repeated.Prospective.Bytes) || incremental.MigrationID != "0002_add_lookup" {
		t.Fatal("incremental migration preparation is not deterministic")
	}
	publish(t, module, incremental)
	assertHistoryArtifacts(t, module, "0002_add_lookup", 2)

	head := current
	_, err := PrepareNew(context.Background(), NewRequest{
		ModuleDir: module, Root: DefaultRoot, Name: "nothing", Model: current, PreviousModel: &head,
		ModelFingerprint: currentFingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: updatedProviders,
	})
	if err == nil || !strings.Contains(err.Error(), "no logical or physical schema changes") {
		t.Fatalf("pure no-op migration error = %v", err)
	}
	state, err := Load(context.Background(), module, DefaultRoot, updatedProviders)
	if err != nil {
		t.Fatal(err)
	}
	for provider, history := range state.Histories {
		if len(history.Manifest.Entries) != 2 || history.Manifest.Entries[1].ID != "0002_add_lookup" {
			t.Fatalf("provider %s history = %#v", provider, history.Manifest.Entries)
		}
	}
}

func TestTamperAndExactDestructiveApprovals(t *testing.T) {
	compiled, providers := socialResult(t)
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
	publish(t, module, initial)
	sqlPath := filepath.Join(module, DefaultRoot, "sqlite", "0001_initial.sql")
	original, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sqlPath, append(original, []byte("-- tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), module, DefaultRoot, providers); err == nil || !strings.Contains(err.Error(), "rewritten") {
		t.Fatalf("tampered immutable SQL error = %v", err)
	}
	if err := os.WriteFile(sqlPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	previous := compiled.Compilation.Model
	current := cloneModel(t, previous)
	var removedModel ir.ModelID
	var removedField ir.FieldID
	for modelIndex := range current.Models {
		if current.Models[modelIndex].Go.Name != "Post" {
			continue
		}
		removedModel = current.Models[modelIndex].ID
		for fieldIndex, field := range current.Models[modelIndex].Fields {
			if field.GoName == "Body" {
				removedField = field.ID
				current.Models[modelIndex].Fields = append(current.Models[modelIndex].Fields[:fieldIndex:fieldIndex], current.Models[modelIndex].Fields[fieldIndex+1:]...)
				break
			}
		}
	}
	currentFingerprint, _ := ir.ModelFingerprint(current)
	droppedProviders := dropField(t, providers, removedModel, removedField)
	_, err = PrepareNew(context.Background(), NewRequest{
		ModuleDir: module, Root: DefaultRoot, Name: "drop_model", Model: current, PreviousModel: &previous,
		ModelFingerprint: currentFingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: droppedProviders,
	})
	if err == nil || !strings.Contains(err.Error(), "risk dataLoss requires --approve") || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("missing exact approval error = %v", err)
	}
	var approvals []migration.OperationID
	for index, provider := range droppedProviders {
		plan, diffErr := migration.Diff(providers[index].Result.Schema, provider.Result.Schema)
		if diffErr != nil {
			t.Fatal(diffErr)
		}
		for _, operation := range plan.Operations {
			if operation.Risk == migration.RiskDataLoss || operation.Risk == migration.RiskManual {
				approvals = append(approvals, operation.ID)
			}
		}
	}
	approved := prepareWithFingerprint(t, module, "drop_model", current, &previous, currentFingerprint, compiled.ContractFingerprint, droppedProviders, approvals)
	if approved.MigrationID != "0002_drop_model" {
		t.Fatalf("approved migration ID = %s", approved.MigrationID)
	}
	reversedProviders := append([]Provider(nil), droppedProviders...)
	for left, right := 0, len(reversedProviders)-1; left < right; left, right = left+1, right-1 {
		reversedProviders[left], reversedProviders[right] = reversedProviders[right], reversedProviders[left]
	}
	reversedApprovals := append([]migration.OperationID(nil), approvals...)
	for left, right := 0, len(reversedApprovals)-1; left < right; left, right = left+1, right-1 {
		reversedApprovals[left], reversedApprovals[right] = reversedApprovals[right], reversedApprovals[left]
	}
	reordered := prepareWithFingerprint(t, module, "drop_model", current, &previous, currentFingerprint, compiled.ContractFingerprint, reversedProviders, reversedApprovals)
	if !bytes.Equal(approved.Prospective.Bytes, reordered.Prospective.Bytes) {
		t.Fatal("provider or approval registry order changed the prospective migration bytes")
	}
	unknown := append(append([]migration.OperationID(nil), approvals...), "unknown")
	if _, err := PrepareNew(context.Background(), NewRequest{
		ModuleDir: module, Root: DefaultRoot, Name: "drop_model", Model: current, PreviousModel: &previous,
		ModelFingerprint: currentFingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: droppedProviders, Approvals: unknown,
	}); err == nil || !strings.Contains(err.Error(), "does not name a current") {
		t.Fatalf("unknown approval error = %v", err)
	}
}

func TestInterruptedPublicationRecoversWithoutPartialHistory(t *testing.T) {
	compiled, providers := socialResult(t)
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
	publish(t, module, initial)
	before := tree(t, module)
	previous := compiled.Compilation.Model
	current := previous
	current.Schema.StableName += "_next"
	fingerprint, _ := ir.ModelFingerprint(current)
	next := prepareWithFingerprint(t, module, "next", current, &previous, fingerprint, compiled.ContractFingerprint, addPortableIndex(t, providers), nil)
	injected := false
	_, err := publication.Apply(context.Background(), publication.Request{
		ModuleDir: module, ManifestPath: next.PublicationPath, Prospective: next.Prospective,
		Inject: func(step publication.Step, _ string) error {
			if !injected && step == publication.StepJournaled {
				injected = true
				return errors.New("simulated crash")
			}
			return nil
		},
	})
	if err == nil || !injected {
		t.Fatalf("publication interruption = %v", err)
	}
	if err := publication.Recover(context.Background(), module, next.PublicationPath); err != nil {
		t.Fatal(err)
	}
	if after := tree(t, module); !reflect.DeepEqual(before, after) {
		t.Fatal("recovery left a partial migration history")
	}
}

func TestMigrationPublicationRejectsStaleSystemFingerprint(t *testing.T) {
	compiled, providers := socialResult(t)
	staleProviders := append([]Provider(nil), providers...)
	staleProviders[0].Result.SystemFingerprint = ir.Fingerprint(strings.Repeat("f", 64))
	if _, err := PrepareNew(context.Background(), NewRequest{
		ModuleDir: t.TempDir(), Root: DefaultRoot, Name: "initial", Model: compiled.Compilation.Model,
		ModelFingerprint: compiled.ModelFingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: staleProviders,
	}); err == nil || !strings.Contains(err.Error(), "stale system fingerprint") {
		t.Fatalf("stale provider input error=%v", err)
	}
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
	publish(t, module, initial)
	publicationPath := filepath.Join(module, DefaultRoot, PublicationFilename)
	encoded, err := os.ReadFile(publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := codegenmanifest.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	fingerprints := append([]codegenmanifest.ProviderFingerprint(nil), current.ProviderFingerprints...)
	fingerprints[0].SystemFingerprint = ir.Fingerprint(strings.Repeat("f", 64))
	rebuilt, err := codegenmanifest.Build(codegenmanifest.Request{
		ModelFingerprint: current.ModelFingerprint, ContractFingerprint: current.ContractFingerprint,
		ProviderFingerprints: fingerprints, GeneratorVersion: current.GeneratorVersion,
		TemplateABIVersion: current.TemplateABIVersion, Artifacts: publicationArtifacts(t, module, current),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.GenerationDigest == current.GenerationDigest {
		t.Fatal("system-only publication fingerprint change did not alter GenerationDigest")
	}
	if err := os.WriteFile(publicationPath, rebuilt.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadModelHead(module, DefaultRoot); err == nil || !strings.Contains(err.Error(), "system fingerprint differs") {
		t.Fatalf("stale system fingerprint error=%v", err)
	}
}

func TestReadModelHeadUsesFullPublicationIntegrityChecks(t *testing.T) {
	compiled, providers := socialResult(t)
	for _, test := range []struct {
		name  string
		add   func(string, codegenmanifest.Manifest) codegenmanifest.Artifact
		match string
	}{
		{
			name: "out of scope",
			add: func(_ string, _ codegenmanifest.Manifest) codegenmanifest.Artifact {
				return codegenmanifest.Artifact{Path: "outside.sql", Kind: codegenmanifest.ArtifactMigrationSQL, Content: []byte("SELECT 1;\n"), Immutable: true}
			},
			match: "out-of-scope",
		},
		{
			name: "duplicate provider manifest",
			add: func(module string, _ codegenmanifest.Manifest) codegenmanifest.Artifact {
				content, err := os.ReadFile(filepath.Join(module, DefaultRoot, "sqlite", "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				return codegenmanifest.Artifact{Path: DefaultRoot + "/sqlite/other-manifest.json", Kind: codegenmanifest.ArtifactMigrationManifest, Content: content}
			},
			match: "duplicate provider manifest",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := t.TempDir()
			initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
			publish(t, module, initial)
			publicationPath := filepath.Join(module, DefaultRoot, PublicationFilename)
			encoded, err := os.ReadFile(publicationPath)
			if err != nil {
				t.Fatal(err)
			}
			current, err := codegenmanifest.Parse(encoded)
			if err != nil {
				t.Fatal(err)
			}
			artifacts := publicationArtifacts(t, module, current)
			extra := test.add(module, current)
			artifacts = append(artifacts, extra)
			absolute := filepath.Join(module, filepath.FromSlash(extra.Path))
			if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(absolute, extra.Content, 0o644); err != nil {
				t.Fatal(err)
			}
			rebuilt, err := codegenmanifest.Build(codegenmanifest.Request{
				ModelFingerprint: current.ModelFingerprint, ContractFingerprint: current.ContractFingerprint,
				ProviderFingerprints: current.ProviderFingerprints, GeneratorVersion: current.GeneratorVersion,
				TemplateABIVersion: current.TemplateABIVersion, Artifacts: artifacts,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(publicationPath, rebuilt.Bytes, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadModelHead(module, DefaultRoot); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("ReadModelHead error = %v; want %q", err, test.match)
			}
		})
	}
}

func socialResult(t *testing.T) (pipeline.Result, []Provider) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	result, err := pipeline.Build(context.Background(), pipeline.Request{
		Compile:  compile.Config{Dir: root, Pattern: "./cmd/golem/testdata/social", Root: "DefineSchema"},
		Lowerers: []physical.Lowerer{sqlite.New(), postgresql.New()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result, providerRegistry(t, result.Providers)
}

func providerRegistry(t *testing.T, results []pipeline.ProviderResult) []Provider {
	t.Helper()
	providers := make([]Provider, 0, len(results))
	for _, result := range results {
		result := result
		switch result.Provider.Provider {
		case ir.SQLite:
			provider := sqlite.New()
			providers = append(providers, Provider{Result: result, Render: func(entry migration.ManifestEntry) ([]byte, error) {
				script, err := provider.RenderMigration(entry)
				return []byte(script.SQL()), err
			}})
		case ir.PostgreSQL:
			provider := postgresql.New()
			providers = append(providers, Provider{Result: result, Render: func(entry migration.ManifestEntry) ([]byte, error) {
				plan, err := migration.Diff(entry.BeforeSnapshot, entry.AfterSnapshot)
				if err != nil {
					return nil, err
				}
				if plan.Initial {
					script, renderErr := provider.RenderInitial(entry.AfterSnapshot)
					return []byte(script.SQL()), renderErr
				}
				script, renderErr := provider.PlanIncremental(entry)
				return []byte(script.SQL()), renderErr
			}})
		default:
			t.Fatalf("unsupported provider %s", result.Provider.Provider)
		}
	}
	return providers
}

func prepare(t *testing.T, module, name string, model ir.ModelIR, previous *ir.ModelIR, contract ir.Fingerprint, providers []Provider, approvals []migration.OperationID) NewResult {
	t.Helper()
	fingerprint, err := ir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	return prepareWithFingerprint(t, module, name, model, previous, fingerprint, contract, providers, approvals)
}

func prepareWithFingerprint(t *testing.T, module, name string, model ir.ModelIR, previous *ir.ModelIR, fingerprint, contract ir.Fingerprint, providers []Provider, approvals []migration.OperationID) NewResult {
	t.Helper()
	result, err := PrepareNew(context.Background(), NewRequest{ModuleDir: module, Root: DefaultRoot, Name: name, Model: model, PreviousModel: previous, ModelFingerprint: fingerprint, ContractFingerprint: contract, Providers: providers, Approvals: approvals})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func publish(t *testing.T, module string, value NewResult) {
	t.Helper()
	if _, err := publication.Apply(context.Background(), publication.Request{ModuleDir: module, ManifestPath: value.PublicationPath, Prospective: value.Prospective}); err != nil {
		t.Fatal(err)
	}
}

func addPortableIndex(t *testing.T, providers []Provider) []Provider {
	t.Helper()
	result := append([]Provider(nil), providers...)
	for index := range result {
		schema := result[index].Result.Schema
		schema.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
		table := schema.Tables[0]
		table.Indexes = append([]physical.PhysicalIndex(nil), table.Indexes...)
		field := table.Columns[0].ID
		table.Indexes = append(table.Indexes, physical.PhysicalIndex{
			ID: "ffffffffffffffffffffffffffffff01", Name: "idx_workflow_lookup", Method: physical.IndexBTree,
			Keys: []physical.IndexKey{{Column: &field, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional,
		})
		schema.Tables[0] = table
		normalized, err := physical.Normalize(schema)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, _ := physical.PhysicalFingerprint(normalized)
		result[index].Result.Schema, result[index].Result.Fingerprint = normalized, ir.Fingerprint(fingerprint.String())
	}
	return result
}

func dropField(t *testing.T, providers []Provider, modelID ir.ModelID, fieldID ir.FieldID) []Provider {
	t.Helper()
	result := append([]Provider(nil), providers...)
	for index := range result {
		schema := result[index].Result.Schema
		schema.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
		for tableIndex := range schema.Tables {
			if schema.Tables[tableIndex].ID != modelID {
				continue
			}
			table := schema.Tables[tableIndex]
			table.Columns = append([]physical.PhysicalColumn(nil), table.Columns...)
			for columnIndex, column := range table.Columns {
				if column.ID == fieldID {
					table.Columns = append(table.Columns[:columnIndex:columnIndex], table.Columns[columnIndex+1:]...)
					break
				}
			}
			for columnIndex := range table.Columns {
				table.Columns[columnIndex].Ordinal = uint32(columnIndex)
			}
			schema.Tables[tableIndex] = table
		}
		normalized, err := physical.Normalize(schema)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, _ := physical.PhysicalFingerprint(normalized)
		result[index].Result.Schema, result[index].Result.Fingerprint = normalized, ir.Fingerprint(fingerprint.String())
	}
	return result
}

func assertHistoryArtifacts(t *testing.T, module, id string, providers int) {
	t.Helper()
	for _, provider := range []string{"postgresql", "sqlite"}[:providers] {
		for _, suffix := range []string{".sql", ".before.snapshot.json", ".after.snapshot.json"} {
			if _, err := os.Stat(filepath.Join(module, DefaultRoot, provider, id+suffix)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, suffix := range []string{".before.snapshot.json", ".after.snapshot.json"} {
		if _, err := os.Stat(filepath.Join(module, DefaultRoot, "models", id+suffix)); err != nil {
			t.Fatal(err)
		}
	}
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, _ := filepath.Rel(root, name)
		content, readErr := os.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		result[filepath.ToSlash(relative)] = string(content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func publicationArtifacts(t *testing.T, module string, value codegenmanifest.Manifest) []codegenmanifest.Artifact {
	t.Helper()
	artifacts := make([]codegenmanifest.Artifact, len(value.Artifacts))
	for index, entry := range value.Artifacts {
		content, err := os.ReadFile(filepath.Join(module, filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		artifacts[index] = codegenmanifest.Artifact{Path: entry.Path, Kind: entry.Kind, Content: content, GeneratedHeader: entry.GeneratedHeader, Immutable: entry.Immutable}
	}
	return artifacts
}

func cloneModel(t *testing.T, value ir.ModelIR) ir.ModelIR {
	t.Helper()
	encoded, err := ir.CanonicalModel(value)
	if err != nil {
		t.Fatal(err)
	}
	var result ir.ModelIR
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
