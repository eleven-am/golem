package explain

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

func TestMigrationExplainProspectiveAdapterUsesOnlyValidatedPlanSnapshotFacts(t *testing.T) {
	before, after := adapterSchemas()
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildProspective(plan)
	if err != nil {
		t.Fatal(err)
	}
	providers := report.Providers()
	if len(providers) != 1 || providers[0].Provider() != ir.SQLite || providers[0].BeforeFingerprint() != plan.BeforeFingerprint || providers[0].AfterFingerprint() != plan.AfterFingerprint {
		t.Fatalf("provider report=%#v", providers)
	}
	var found bool
	for _, phase := range providers[0].Phases() {
		for _, operation := range phase.Operations() {
			if operation.Kind() == migration.AddColumn {
				found = true
				if operation.Identity().ModelID() != after.Tables[0].ID || operation.Identity().FieldID() != after.Tables[0].Columns[1].ID || operation.Display() != "" || operation.Effect() != EffectSchemaOnly {
					t.Fatalf("typed add-column explanation=%#v identity=%#v", operation, operation.Identity())
				}
			}
		}
	}
	if !found {
		t.Fatal("add-column explanation absent")
	}
	text, _ := MarshalText(report)
	machine, _ := MarshalJSON(report)
	for _, private := range [][]byte{[]byte("physical_private_posts"), []byte("physical_private_subtitle")} {
		if bytes.Contains(text, private) || bytes.Contains(machine, private) {
			t.Fatalf("physical name leaked: %q", private)
		}
	}

	forged := plan
	forged.Operations = append([]migration.Operation(nil), plan.Operations...)
	forged.Operations[0].Risk = migration.RiskLocking
	if _, err := BuildProspective(forged); !isCode(err, codeUnavailable) {
		t.Fatalf("forged plan error=%v", err)
	}
}

func TestMigrationExplainProspectiveAdapterDoesNotCallUnsafeCurrentTypeChangePreserving(t *testing.T) {
	modelID := ir.ModelID("84000000000000000000000000000001")
	fieldID := ir.FieldID("84000000000000000000000000000002")
	before := physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: physical.PostgreSQLManifest(), Namespace: physical.Namespace{Name: "public"},
		Tables: []physical.PhysicalTable{{ID: modelID, Name: "private_items", Columns: []physical.PhysicalColumn{{
			ID: fieldID, Name: "private_value", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone},
		}}}},
	}
	after := before
	after.Tables = append([]physical.PhysicalTable(nil), before.Tables...)
	after.Tables[0].Columns = append([]physical.PhysicalColumn(nil), before.Tables[0].Columns...)
	after.Tables[0].Columns[0].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: 10}
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildProspective(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range report.Providers()[0].Phases() {
		for _, operation := range phase.Operations() {
			if operation.Kind() == migration.AlterColumnType {
				if operation.Effect() != EffectUnknown || !containsWarning(operation.Warnings(), WarningManualReview) {
					t.Fatalf("unsafe current type change effect=%q warnings=%v", operation.Effect(), operation.Warnings())
				}
				return
			}
		}
	}
	t.Fatal("alter-column-type operation absent")
}

func TestMigrationExplainProspectiveAdapterUsesOperationLocalPresenceForRecreations(t *testing.T) {
	before := adapterGeneratedRecreationSchema(160)
	after := adapterGeneratedRecreationSchema(320)
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildProspective(plan)
	if err != nil {
		t.Fatalf("generated-column recreation report: %v", err)
	}
	want := map[migration.OperationKind]Effect{
		migration.DropColumn: EffectValueRewritten, migration.AddColumn: EffectValueRewritten,
		migration.DropPrimaryKey: EffectSchemaOnly, migration.AddPrimaryKey: EffectSchemaOnly,
		migration.DropUnique: EffectSchemaOnly, migration.AddUnique: EffectSchemaOnly,
		migration.DropForeignKey: EffectSchemaOnly, migration.AddForeignKey: EffectSchemaOnly,
		migration.DropCheck: EffectSchemaOnly, migration.AddCheck: EffectSchemaOnly,
		migration.DropIndex: EffectSchemaOnly, migration.CreateIndex: EffectSchemaOnly,
	}
	seen := map[migration.OperationKind]bool{}
	for _, phase := range report.Providers()[0].Phases() {
		for _, operation := range phase.Operations() {
			effect, exists := want[operation.Kind()]
			if !exists {
				continue
			}
			seen[operation.Kind()] = true
			if operation.Effect() != effect {
				t.Fatalf("recreated %s effect=%s want=%s", operation.Kind(), operation.Effect(), effect)
			}
		}
	}
	for kind := range want {
		if !seen[kind] {
			t.Fatalf("recreation matrix lacks %s: %#v", kind, plan.Operations)
		}
	}
}

func TestMigrationExplainProspectiveAdapterPreservesProviderExtensionRecreationRewrite(t *testing.T) {
	const (
		modelID     = ir.ModelID("85000000000000000000000000000001")
		identityID  = ir.FieldID("85000000000000000000000000000002")
		titleID     = ir.FieldID("85000000000000000000000000000003")
		bodyID      = ir.FieldID("85000000000000000000000000000004")
		extensionID = ir.ExtensionID("85000000000000000000000000000005")
	)
	base := physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: physical.SQLiteManifest(), Namespace: physical.Namespace{Name: "main"},
		Tables: []physical.PhysicalTable{{ID: modelID, Name: "private_posts", Columns: []physical.PhysicalColumn{
			{ID: identityID, Name: "private_id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: titleID, Name: "private_title", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: bodyID, Name: "private_body", Ordinal: 2, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		}}},
	}
	extension := func(dimensions uint16, fields ...ir.FieldID) physical.Extension {
		values := make([]string, len(fields))
		for index := range fields {
			values[index] = string(fields[index])
		}
		payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: dimensions, Fields: values, Metric: "cosine"})
		if err != nil {
			t.Fatal(err)
		}
		value, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: extensionID, Provider: ir.SQLite, Version: 1, Owner: ir.ObjectID(modelID), Kind: semanticcontract.IndexKind, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before, after := base, base
	before.Extensions = []physical.Extension{extension(3, titleID)}
	after.Extensions = []physical.Extension{extension(4, titleID, bodyID)}
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildProspective(plan)
	if err != nil {
		t.Fatalf("provider-extension recreation report: %v", err)
	}
	seen := map[migration.OperationKind]bool{}
	for _, operation := range report.Providers()[0].Phases()[0].Operations() {
		if operation.Kind() == migration.DropProviderExtension || operation.Kind() == migration.CreateProviderExtension {
			seen[operation.Kind()] = true
			if operation.Effect() != EffectValueRewritten {
				t.Fatalf("provider-extension recreation %s effect=%s", operation.Kind(), operation.Effect())
			}
		}
	}
	if !seen[migration.DropProviderExtension] || !seen[migration.CreateProviderExtension] {
		t.Fatalf("provider-extension recreation pair absent: %#v", plan.Operations)
	}
}

func TestMigrationExplainOperationLocalColumnPresenceUsesTheOperationSide(t *testing.T) {
	const fieldID = ir.FieldID("86000000000000000000000000000004")
	generated := adapterGeneratedRecreationSchema(160)
	plain := adapterGeneratedRecreationSchema(160)
	plain.Tables[0].Columns[1].Generated = nil
	for _, test := range []struct {
		name      string
		before    physical.PhysicalSchema
		after     physical.PhysicalSchema
		operation migration.Operation
	}{
		{name: "drop reads left", before: generated, after: plain, operation: migration.Operation{Kind: migration.DropColumn, ObjectID: string(fieldID), Before: migration.Checksum([]byte("before")), Risk: migration.RiskRewrite}},
		{name: "add reads right", before: plain, after: generated, operation: migration.Operation{Kind: migration.AddColumn, ObjectID: string(fieldID), After: migration.Checksum([]byte("after")), Risk: migration.RiskRewrite}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, ok := buildSnapshotIndex(test.before)
			if !ok {
				t.Fatal("before snapshot index")
			}
			after, ok := buildSnapshotIndex(test.after)
			if !ok {
				t.Fatal("after snapshot index")
			}
			_, facts, ok := operationFacts(migration.Plan{Provider: ir.PostgreSQL}, test.operation, before, after)
			if !ok || facts.preservation != preservationRewrite {
				t.Fatalf("operation-local generated side facts=%#v ok=%v", facts, ok)
			}
		})
	}
}

func TestMigrationExplainReviewedAdapterValidatesSealedEntryAndArtifactsBeforeRendering(t *testing.T) {
	before, after := adapterSchemas()
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("reviewed artifact bytes\n")
	entry := migration.ManifestEntry{
		ID: "0002_add_subtitle", ParentID: "0001_initial",
		ParentChainHash: digest('9'), Files: []migration.FileChecksum{{Path: "migrations/sqlite/0002_add_subtitle.sql", SHA256: migration.Checksum(artifact)}},
		Operations: append([]migration.Operation(nil), plan.Operations...), Phases: append([]migration.Phase(nil), plan.Phases...),
		BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint,
		BeforeSnapshot: before, AfterSnapshot: after,
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	entry.ChainHash = migration.ChainHash(entry)
	files := map[string][]byte{entry.Files[0].Path: artifact}
	report, err := BuildReviewed(entry, files)
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode() != ModeReviewed || len(report.Providers()[0].Artifacts()) != 1 {
		t.Fatalf("reviewed report=%#v", report)
	}

	tampered := entry
	tampered.Phases = append([]migration.Phase(nil), entry.Phases...)
	tampered.Phases[0].Operations = append([]migration.OperationID(nil), entry.Phases[0].Operations...)
	tampered.Phases[0].Operations[0], tampered.Phases[0].Operations[1] = tampered.Phases[0].Operations[1], tampered.Phases[0].Operations[0]
	tampered.ChainHash = migration.ChainHash(tampered)
	if _, err := BuildReviewed(tampered, files); !isCode(err, codeUnavailable) {
		t.Fatalf("reordered sealed entry error=%v", err)
	}

	rewritten := map[string][]byte{entry.Files[0].Path: []byte("rewritten\n")}
	if _, err := BuildReviewed(entry, rewritten); !isCode(err, codeUnavailable) {
		t.Fatalf("rewritten artifact error=%v", err)
	}
}

func TestMigrationExplainAdaptersRefuseOversizedInputsBeforeAdaptation(t *testing.T) {
	before, after := adapterSchemas()
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	tooManyProviders := make([]migration.Plan, maxProviders+1)
	for index := range tooManyProviders {
		tooManyProviders[index] = plan
	}
	if _, err := BuildProspectiveAll(tooManyProviders); !isCode(err, codeUnavailable) {
		t.Fatalf("oversized provider inventory error=%v", err)
	}
	overflow := plan
	overflow.Operations = make([]migration.Operation, maxCollectionItems+1)
	if _, err := BuildProspectiveAll([]migration.Plan{overflow}); !isCode(err, codeUnavailable) {
		t.Fatalf("oversized operation inventory error=%v", err)
	}
	overflow = plan
	overflow.Operations = append([]migration.Operation(nil), plan.Operations...)
	overflow.Operations[0].Dependencies = make([]migration.OperationID, maxCollectionItems+1)
	if _, err := BuildProspectiveAll([]migration.Plan{overflow}); !isCode(err, codeUnavailable) {
		t.Fatalf("oversized dependency inventory error=%v", err)
	}
	values := make([]ReviewedInput, maxProviders+1)
	if _, err := BuildReviewedAll(values); !isCode(err, codeUnavailable) {
		t.Fatalf("oversized reviewed provider inventory error=%v", err)
	}
	entry := migration.ManifestEntry{Files: make([]migration.FileChecksum, maxCollectionItems+1)}
	if _, err := BuildReviewedAll([]ReviewedInput{{Entry: entry}}); !isCode(err, codeUnavailable) {
		t.Fatalf("oversized reviewed file inventory error=%v", err)
	}
}

func adapterSchemas() (physical.PhysicalSchema, physical.PhysicalSchema) {
	modelID := ir.ModelID("83000000000000000000000000000001")
	id := ir.FieldID("83000000000000000000000000000002")
	subtitle := ir.FieldID("83000000000000000000000000000003")
	before := physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: physical.SQLiteManifest(), Namespace: physical.Namespace{Name: "main"},
		Tables: []physical.PhysicalTable{{
			ID: modelID, Name: "physical_private_posts",
			Columns: []physical.PhysicalColumn{{ID: id, Name: "physical_private_id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}},
		}},
	}
	after := before
	after.Tables = append([]physical.PhysicalTable(nil), before.Tables...)
	after.Tables[0].Columns = append([]physical.PhysicalColumn(nil), before.Tables[0].Columns...)
	after.Tables[0].Columns = append(after.Tables[0].Columns, physical.PhysicalColumn{ID: subtitle, Name: "physical_private_subtitle", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	return before, after
}

func adapterGeneratedRecreationSchema(sourceLength uint32) physical.PhysicalSchema {
	const (
		parent  = ir.ModelID("86000000000000000000000000000001")
		child   = ir.ModelID("86000000000000000000000000000002")
		source  = ir.FieldID("86000000000000000000000000000003")
		first   = ir.FieldID("86000000000000000000000000000004")
		second  = ir.FieldID("86000000000000000000000000000005")
		childID = ir.FieldID("86000000000000000000000000000006")
	)
	bounded := func(length uint32) physical.StorageType {
		return physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: length}
	}
	lower := func(input ir.FieldID, inputType, outputType physical.StorageType) *physical.GeneratedExpression {
		field := input
		return &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{
			Kind: physical.ExpressionFunction, Type: outputType,
			Symbol:   &physical.SemanticSymbol{Identity: "golem.schema.function.lower.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable},
			Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: inputType, Column: &field, Operands: []physical.Expression{}}},
		}}
	}
	stable := bounded(160)
	firstField := first
	notNullFirst := physical.Expression{
		Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean},
		Symbol:   &physical.SemanticSymbol{Identity: "golem.schema.predicate.is-not-null.v1", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopePortable},
		Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: stable, Column: &firstField, Operands: []physical.Expression{}}},
	}
	return physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: physical.PostgreSQLManifest(), Namespace: physical.Namespace{Name: "public"},
		Tables: []physical.PhysicalTable{
			{ID: parent, Name: "private_parents", Columns: []physical.PhysicalColumn{
				{ID: source, Name: "private_source", Storage: bounded(sourceLength), Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
				{ID: first, Name: "private_first", Ordinal: 1, Storage: stable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}, Generated: lower(source, bounded(sourceLength), stable)},
				{ID: second, Name: "private_second", Ordinal: 2, Storage: stable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}, Generated: lower(source, bounded(sourceLength), stable)},
			},
				PrimaryKey: &physical.PhysicalKey{ID: "86000000000000000000000000000007", Name: "private_pk", Columns: []ir.FieldID{second}},
				Uniques:    []physical.PhysicalKey{{ID: "86000000000000000000000000000008", Name: "private_uq", Columns: []ir.FieldID{first}}},
				Checks:     []physical.PhysicalCheck{{ID: "86000000000000000000000000000009", Name: "private_ck", Expression: notNullFirst}},
				Indexes:    []physical.PhysicalIndex{{ID: "8600000000000000000000000000000a", Name: "private_idx", Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &firstField, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional}},
			},
			{ID: child, Name: "private_children", Columns: []physical.PhysicalColumn{
				{ID: childID, Name: "private_parent", Storage: stable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			}, ForeignKeys: []physical.PhysicalForeignKey{{ID: "8600000000000000000000000000000b", Name: "private_fk", Columns: []ir.FieldID{childID}, ReferencedTable: parent, ReferencedColumns: []ir.FieldID{second}, OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionRestrict, Deferrable: ir.NotDeferrable}}},
		},
	}
}
