package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

func releasedSocialManifests(t *testing.T) map[string]Manifest {
	t.Helper()
	result := map[string]Manifest{}
	for _, provider := range []string{"sqlite", "postgresql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "social", "migrations", provider, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		if len(manifest.Entries) == 0 {
			t.Fatalf("provider %s released manifest is empty", provider)
		}
		result[provider] = manifest
	}
	return result
}

func semanticStateUpgradeSchemas(t *testing.T) (physical.PhysicalSchema, physical.PhysicalSchema) {
	t.Helper()
	modelID := ir.ModelID("0123456789abcdef0123456789abcdef")
	identityID := ir.FieldID("1123456789abcdef0123456789abcdef")
	titleID := ir.FieldID("1223456789abcdef0123456789abcdef")
	base := schema()
	base.Tables = []physical.PhysicalTable{{
		ID: modelID, Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: identityID, Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: titleID, Name: "title", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "2123456789abcdef0123456789abcdef", Name: "pk_posts", Columns: []ir.FieldID{identityID}},
	}}
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{string(titleID)}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: "3123456789abcdef0123456789abcdef", Provider: ir.SQLite, Version: 1, Owner: ir.ObjectID(modelID), Kind: semanticcontract.IndexKind, Payload: payload}, base.Tables[0])
	if err != nil {
		t.Fatal(err)
	}
	original := upgraded
	original.Attributes = nil
	for _, attribute := range upgraded.Attributes {
		if attribute.Name != "state_version" {
			original.Attributes = append(original.Attributes, attribute)
		}
	}
	before, after := base, base
	before.Extensions = []physical.Extension{original}
	after.Extensions = []physical.Extension{upgraded}
	return before, after
}

func TestRegisteredShadowStateUpgradeIsPlannedInPlaceAndNeverAsARewrite(t *testing.T) {
	before, after := semanticStateUpgradeSchemas(t)
	current, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := DiffHistoricalV3(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current, frozen) {
		t.Fatalf("frozen v3 planner differs from the current planner for a shadow state upgrade\ncurrent=%#v\nfrozen=%#v", current.Operations, frozen.Operations)
	}
	reviewed, err := DiffReviewed(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reviewed, frozen) {
		t.Fatal("reviewed dispatch bypassed the retained v3 planner")
	}
	var upgrade Operation
	for _, operation := range frozen.Operations {
		switch operation.Kind {
		case DropProviderExtension, CreateProviderExtension:
			t.Fatalf("a registered additive upgrade was planned as a rewrite: %#v", frozen.Operations)
		case UpgradeSemanticState:
			if upgrade.ID != "" {
				t.Fatal("more than one shadow state upgrade was planned")
			}
			upgrade = operation
		}
	}
	if upgrade.ID == "" {
		t.Fatalf("no shadow state upgrade was planned: %#v", frozen.Operations)
	}
	if upgrade.Risk != RiskSafe || upgrade.Mode != Transactional {
		t.Fatalf("shadow state upgrade risk=%s mode=%s", upgrade.Risk, upgrade.Mode)
	}
	if RequiresApproval(upgrade) {
		t.Fatal("an additive shadow state upgrade demands an approval")
	}
	if upgrade.Before == "" || upgrade.After == "" || upgrade.Before == upgrade.After {
		t.Fatalf("shadow state upgrade does not bind both contracts: %#v", upgrade)
	}
}

func TestShadowStateUpgradeIsUnreachableWithoutTheVersionAttribute(t *testing.T) {
	before, after := semanticStateUpgradeSchemas(t)
	identical, err := Diff(before, before)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range identical.Operations {
		if operation.Kind == UpgradeSemanticState {
			t.Fatal("an unchanged pair planned a shadow state upgrade")
		}
	}
	reversed, err := Diff(after, before)
	if err != nil {
		t.Fatal(err)
	}
	upgrades, rewrites := 0, 0
	for _, operation := range reversed.Operations {
		switch operation.Kind {
		case UpgradeSemanticState:
			upgrades++
		case DropProviderExtension, CreateProviderExtension:
			rewrites++
		}
	}
	if upgrades != 0 || rewrites != 2 {
		t.Fatalf("removing the strike column was not planned as a rewrite: upgrades=%d rewrites=%d", upgrades, rewrites)
	}
}

func TestNoReleasedSnapshotCarriesTheShadowStateVersionAttribute(t *testing.T) {
	for provider, manifest := range releasedSocialManifests(t) {
		for _, entry := range manifest.Entries {
			for _, snapshot := range []physical.PhysicalSchema{entry.BeforeSnapshot, entry.AfterSnapshot} {
				for _, extension := range snapshot.Extensions {
					for _, attribute := range extension.Attributes {
						if attribute.Name == "state_version" {
							t.Fatalf("provider %s migration %s extension %s already carries state_version", provider, entry.ID, extension.ID)
						}
					}
				}
			}
		}
	}
}

func TestReleasedSocialChainsReplayToTheirRecordedOperationGraph(t *testing.T) {
	for provider, manifest := range releasedSocialManifests(t) {
		for _, entry := range manifest.Entries {
			plan, err := DiffReviewed(entry.BeforeSnapshot, entry.AfterSnapshot)
			if err != nil {
				t.Fatalf("provider %s migration %s replay: %v", provider, entry.ID, err)
			}
			if plan.BeforeFingerprint != entry.BeforePhysical || plan.AfterFingerprint != entry.AfterPhysical {
				t.Fatalf("provider %s migration %s replay fingerprints drifted", provider, entry.ID)
			}
			if !reflect.DeepEqual(plan.Operations, entry.Operations) {
				t.Fatalf("provider %s migration %s replay operations differ from the released graph", provider, entry.ID)
			}
			if !reflect.DeepEqual(plan.Phases, entry.Phases) {
				t.Fatalf("provider %s migration %s replay phases differ from the released graph", provider, entry.ID)
			}
		}
	}
}
