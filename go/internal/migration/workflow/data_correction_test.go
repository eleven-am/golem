package workflow

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/migration/explain"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestDataCorrectionPublishesSeparateImmutableHistory(t *testing.T) {
	fixture := schematest.WithDataIncarnation(t, schematest.NewSubscribedGraph(t))
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, _ := ir.ModelFingerprint(compilation.Model)
	contractFingerprint, _ := ir.ContractFingerprint(compilation.Contract)
	for _, snapshot := range []physical.PhysicalSchema{fixture.SQLite, fixture.PostgreSQL} {
		t.Run(string(snapshot.Provider.Provider), func(t *testing.T) {
			ctx := context.Background()
			fingerprint, _ := physical.PhysicalFingerprint(snapshot)
			systemFingerprint, _ := physical.SystemFingerprint(snapshot.Provider, snapshot.System)
			providers := providerRegistry(t, []pipeline.ProviderResult{{Provider: snapshot.Provider, Schema: snapshot, Fingerprint: ir.Fingerprint(fingerprint.String()), SystemFingerprint: ir.Fingerprint(systemFingerprint.String())}})
			module := t.TempDir()
			request := NewRequest{ModuleDir: module, Root: DefaultRoot, Name: "initial", Model: compilation.Model, ModelFingerprint: modelFingerprint, ContractFingerprint: contractFingerprint, Providers: providers}
			initial, err := PrepareNew(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			publish(t, module, initial)
			before := tree(t, module)
			request.Name, request.PreviousModel = "correct_title", &compilation.Model
			if _, err := PrepareNew(ctx, request); err == nil {
				t.Fatal("ordinary schema migration accepted unchanged schemas")
			}
			plan, err := migration.DataCorrectionPlan(snapshot, ir.FieldID(hex.EncodeToString(fixture.PostTitle[:])))
			if err != nil {
				t.Fatal(err)
			}
			correction := DataCorrectionRequest{NewRequest: request, Field: "Post.Title", SQL: []byte("UPDATE posts SET title='fixed';\n")}
			if snapshot.Provider.Provider == ir.PostgreSQL {
				correction.SQL = []byte("UPDATE public.posts SET title='fixed';\n")
			}
			if _, err := PrepareDataCorrection(ctx, correction); err == nil || !strings.Contains(err.Error(), "--approve") {
				t.Fatalf("unapproved correction: %v", err)
			}
			correction.Approvals = []migration.OperationID{plan.Operations[0].ID}
			prepared, err := PrepareDataCorrection(ctx, correction)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, tree(t, module)) {
				t.Fatal("preparation modified reviewed history")
			}
			publish(t, module, prepared)
			state, err := Load(ctx, module, DefaultRoot, providers)
			if err != nil {
				t.Fatal(err)
			}
			history := state.Histories[snapshot.Provider.Provider]
			if len(history.Manifest.Entries) != 2 {
				t.Fatal("correction did not append exactly one reviewed entry")
			}
			entry := history.Manifest.Entries[1]
			if !migration.IsDataCorrection(entry) || entry.BeforeModel != entry.AfterModel || entry.BeforePhysical != entry.AfterPhysical || !reflect.DeepEqual(entry.BeforeSnapshot, entry.AfterSnapshot) {
				t.Fatal("data correction changed schema identity")
			}
			if _, err := explain.BuildReviewed(entry, history.Files); err != nil {
				t.Fatalf("reviewed correction cannot be explained: %v", err)
			}
			for path, content := range before {
				if strings.Contains(path, "0001_initial") && content != tree(t, module)[path] {
					t.Fatalf("rewrote immutable initial artifact %s", path)
				}
			}
			filename := filepath.Join(module, filepath.FromSlash(entry.Manual[0].File.Path))
			if err := os.WriteFile(filename, []byte("UPDATE posts SET title='tampered';\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(ctx, module, DefaultRoot, providers); err == nil {
				t.Fatal("rewritten correction companion accepted")
			}
		})
	}
}
