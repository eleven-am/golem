package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/testenv"
)

const evolutionAuthorID = "00000000-0000-4000-8000-000000000001"
const evolutionNoteID = "00000000-0000-4000-8000-000000000002"

type evolutionSchema struct {
	authorsTable   string
	authorIDColumn string
	handleType     string
	titleType      string
	titleOptional  bool
	bodyColumn     string
	titleColumn    string
	noteFields     []string
	providers      string
}

func defaultEvolutionSchema() evolutionSchema {
	return evolutionSchema{
		authorsTable:   "authors",
		authorIDColumn: "id",
		handleType:     "varchar(40)",
		titleType:      "varchar(200)",
		bodyColumn:     "body",
		titleColumn:    "title",
		noteFields:     []string{"ID", "AuthorID", "Title", "Body", "Author"},
		providers:      "golem.SQLite, golem.PostgreSQL",
	}
}

func (schema evolutionSchema) source() string {
	titleGoType := "string"
	titleType := ""
	if schema.titleType != "" {
		titleType = ";type=" + schema.titleType
	}
	if schema.titleOptional {
		titleGoType = "*string"
	}
	noteFields := map[string]string{
		"ID":       "ID golem.UUID `db:\"id\" golem:\"id=evolution.Note.ID;pk;default=uuid\"`",
		"AuthorID": "AuthorID golem.UUID `db:\"author_id\" golem:\"id=evolution.Note.AuthorID\"`",
		"Title":    "Title " + titleGoType + " `db:\"" + schema.titleColumn + "\" golem:\"id=evolution.Note.Title" + titleType + "\"`",
		"Body":     "Body string `db:\"" + schema.bodyColumn + "\" golem:\"id=evolution.Note.Body\"`",
		"Slug":     "Slug string `db:\"slug\" golem:\"id=evolution.Note.Slug\"`",
		"Rating":   "Rating int32 `db:\"rating\" golem:\"id=evolution.Note.Rating\"`",
		"Summary":  "Summary *string `db:\"summary\" golem:\"id=evolution.Note.Summary\"`",
		"Author":   "Author *Author `db:\"-\" golem:\"relation=belongs_to;fields=author_id;references=" + schema.authorIDColumn + "\"`",
	}
	var fields []string
	for _, name := range schema.noteFields {
		fields = append(fields, "\t"+noteFields[name])
	}
	return `package evolution

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }

type Author struct {
	_ struct{} ` + "`golem:\"model;id=evolution.Author;table=" + schema.authorsTable + "\"`" + `
	ID     golem.UUID ` + "`db:\"" + schema.authorIDColumn + "\" golem:\"id=evolution.Author.ID;pk;default=uuid\"`" + `
	Handle string     ` + "`db:\"handle\" golem:\"id=evolution.Author.Handle;type=" + schema.handleType + "\"`" + `
	Notes  []Note     ` + "`db:\"-\" golem:\"relation=has_many;fields=" + schema.authorIDColumn + ";references=author_id\"`" + `
}

type Note struct {
	_ struct{} ` + "`golem:\"model;id=evolution.Note;table=notes\"`" + `
` + strings.Join(fields, "\n") + `
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "evolution")
	golem.Actor[Actor](schema)
	golem.Model[Author](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, ` + schema.providers + `)
}

func (Author) DefinePolicy(rules *golem.Rules[Author], actor Actor) {
	_ = rules
	_ = actor
}

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	_ = rules
	_ = actor
}
`
}

type evolution struct {
	t         *testing.T
	module    string
	schema    evolutionSchema
	databases map[ir.Provider]string
}

func newEvolution(t *testing.T, schema evolutionSchema) *evolution {
	t.Helper()
	module := t.TempDir()
	goMod := "module example.test/evolution\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(commandModuleRoot(t)) + "\n"
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	value := &evolution{t: t, module: module, databases: map[ir.Provider]string{}}
	value.write(schema)
	for _, provider := range value.providers() {
		switch provider {
		case ir.SQLite:
			value.databases[provider] = filepath.Join(t.TempDir(), "evolution.db")
		case ir.PostgreSQL:
			value.databases[provider] = testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
		}
	}
	value.mustMigrate("initial")
	value.exec(`INSERT INTO ` + value.qualified(schema.authorsTable) + ` ("` + schema.authorIDColumn + `","handle") VALUES ('` + evolutionAuthorID + `','ann')`)
	value.exec(`INSERT INTO ` + value.qualified("notes") + ` ("id","author_id","title","body") VALUES ('` + evolutionNoteID + `','` + evolutionAuthorID + `','first','kept')`)
	return value
}

func (value *evolution) providers() []ir.Provider {
	var result []ir.Provider
	if strings.Contains(value.schema.providers, "SQLite") {
		result = append(result, ir.SQLite)
	}
	if strings.Contains(value.schema.providers, "PostgreSQL") {
		result = append(result, ir.PostgreSQL)
	}
	return result
}

func (value *evolution) write(schema evolutionSchema) {
	value.t.Helper()
	value.schema = schema
	if err := os.WriteFile(filepath.Join(value.module, "schema.go"), []byte(schema.source()), 0o644); err != nil {
		value.t.Fatal(err)
	}
}

func (value *evolution) run(arguments ...string) (int, string, string) {
	value.t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), value.module, arguments, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (value *evolution) requiredApprovals() []string {
	value.t.Helper()
	code, stdout, stderr := value.run("migration", "plan", "--json")
	if code != 0 {
		value.t.Fatalf("migration plan code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var plan migrationPlanJSON
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		value.t.Fatal(err)
	}
	seen := map[string]bool{}
	var approvals []string
	for _, provider := range plan.Providers {
		for _, phase := range provider.Phases {
			for _, operation := range phase.Operations {
				if operation.Approval.Required && !seen[operation.ID] {
					seen[operation.ID] = true
					approvals = append(approvals, operation.ID)
				}
			}
		}
	}
	sort.Strings(approvals)
	return approvals
}

func (value *evolution) newMigration(name string, approvals []string) (int, string, string) {
	value.t.Helper()
	arguments := []string{"migration", "new", "--name", name}
	for _, approval := range approvals {
		arguments = append(arguments, "--approve", approval)
	}
	return value.run(arguments...)
}

func (value *evolution) apply(provider ir.Provider) (int, string, string) {
	value.t.Helper()
	return value.run("migration", "apply", "--provider", string(provider), "--dsn", value.databases[provider])
}

func (value *evolution) mustMigrate(name string) {
	value.t.Helper()
	if code, stdout, stderr := value.newMigration(name, value.requiredApprovals()); code != 0 {
		value.t.Fatalf("migration new %s code=%d stdout=%s stderr=%s", name, code, stdout, stderr)
	}
	for _, provider := range value.providers() {
		if code, stdout, stderr := value.apply(provider); code != 0 {
			value.t.Fatalf("migration apply %s on %s code=%d stdout=%s stderr=%s", name, provider, code, stdout, stderr)
		}
		value.assertSchemaCurrent(provider)
	}
}

func (value *evolution) assertSchemaCurrent(provider ir.Provider) {
	value.t.Helper()
	_, stdout, stderr := value.run("doctor", "--provider", string(provider), "--dsn", value.databases[provider], "--json")
	var output doctorOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		value.t.Fatalf("doctor %s output=%s stderr=%s: %v", provider, stdout, stderr, err)
	}
	if output.History != "current" || output.Schema != "current" {
		value.t.Fatalf("doctor %s history=%s schema=%s diagnostics=%#v", provider, output.History, output.Schema, output.Diagnostics)
	}
}

func (value *evolution) qualified(table string) string {
	return `"` + table + `"`
}

func (value *evolution) exec(statement string) {
	value.t.Helper()
	for _, provider := range value.providers() {
		qualified := statement
		if provider == ir.PostgreSQL {
			qualified = strings.ReplaceAll(statement, "INSERT INTO \"", "INSERT INTO \"public\".\"")
		}
		database, err := openDoctorDatabasePublic(context.Background(), provider, value.dataSource(provider))
		if err != nil {
			value.t.Fatal(err)
		}
		_, execErr := database.UnsafeSQLX().Exec(qualified)
		closeErr := database.Close()
		if execErr != nil {
			value.t.Fatalf("%s: %s: %v", provider, qualified, execErr)
		}
		if closeErr != nil {
			value.t.Fatal(closeErr)
		}
	}
}

func (value *evolution) query(provider ir.Provider, statement string) string {
	value.t.Helper()
	database, err := openDoctorDatabasePublic(context.Background(), provider, value.dataSource(provider))
	if err != nil {
		value.t.Fatal(err)
	}
	defer database.Close()
	var result string
	if err := database.UnsafeSQLX().Get(&result, statement); err != nil {
		value.t.Fatalf("%s: %s: %v", provider, statement, err)
	}
	return result
}

func (value *evolution) dataSource(provider ir.Provider) string {
	if provider == ir.SQLite {
		return "file:" + value.databases[provider]
	}
	return value.databases[provider]
}

func (value *evolution) assertRowsKept(authorsTable, authorIDColumn string) {
	value.t.Helper()
	for _, provider := range value.providers() {
		prefix := ""
		if provider == ir.PostgreSQL {
			prefix = `"public".`
		}
		got := value.query(provider, `SELECT a."handle" || '/' || n."`+value.schema.titleColumn+`" || '/' || n."`+value.schema.bodyColumn+`" FROM `+prefix+`"notes" n JOIN `+prefix+`"`+authorsTable+`" a ON a."`+authorIDColumn+`" = n."author_id"`)
		if got != "ann/first/kept" {
			value.t.Fatalf("%s rows after migration = %q", provider, got)
		}
	}
}

func TestMigrationRenamesAReferencedTableOnEveryProvider(t *testing.T) {
	value := newEvolution(t, defaultEvolutionSchema())
	renamed := value.schema
	renamed.authorsTable = "writers"
	value.write(renamed)
	value.mustMigrate("rename_authors")
	value.assertRowsKept("writers", "id")
	value.exec(`INSERT INTO "notes" ("id","author_id","title","body") VALUES ('00000000-0000-4000-8000-000000000003','` + evolutionAuthorID + `','second','kept')`)
}

func TestMigrationRenamesAReferencedKeyColumnOnEveryProvider(t *testing.T) {
	value := newEvolution(t, defaultEvolutionSchema())
	renamed := value.schema
	renamed.authorIDColumn = "author_key"
	value.write(renamed)
	value.mustMigrate("rename_author_key")
	value.assertRowsKept("authors", "author_key")
	value.exec(`INSERT INTO "notes" ("id","author_id","title","body") VALUES ('00000000-0000-4000-8000-000000000003','` + evolutionAuthorID + `','second','kept')`)
}

func TestMigrationReordersFieldsOnEveryProvider(t *testing.T) {
	value := newEvolution(t, defaultEvolutionSchema())
	reordered := value.schema
	reordered.noteFields = []string{"ID", "AuthorID", "Body", "Title", "Author"}
	value.write(reordered)
	value.mustMigrate("reorder_note_fields")
	value.assertRowsKept("authors", "id")
	value.exec(`INSERT INTO "notes" ("id","author_id","title","body") VALUES ('00000000-0000-4000-8000-000000000003','` + evolutionAuthorID + `','second','kept')`)
}

func TestMigrationInsertsAFieldBeforeExistingFieldsOnEveryProvider(t *testing.T) {
	value := newEvolution(t, defaultEvolutionSchema())
	inserted := value.schema
	inserted.noteFields = []string{"ID", "AuthorID", "Summary", "Title", "Body", "Author"}
	value.write(inserted)
	value.mustMigrate("add_note_summary")
	value.assertRowsKept("authors", "id")
	value.exec(`INSERT INTO "notes" ("id","author_id","summary","title","body") VALUES ('00000000-0000-4000-8000-000000000003','` + evolutionAuthorID + `','short','second','kept')`)
}

func TestMigrationChangesAStringLengthOnEveryProvider(t *testing.T) {
	for _, target := range []string{"varchar(500)", "unbounded"} {
		t.Run("to "+target, func(t *testing.T) {
			value := newEvolution(t, defaultEvolutionSchema())
			widened := value.schema
			widened.titleType = strings.TrimPrefix(target, "unbounded")
			value.write(widened)
			value.mustMigrate("widen_note_title")
			value.assertRowsKept("authors", "id")
			value.exec(`INSERT INTO "notes" ("id","author_id","title","body") VALUES ('00000000-0000-4000-8000-000000000003','` + evolutionAuthorID + `','` + strings.Repeat("x", 300) + `','kept')`)
		})
	}
}

func (value *evolution) plan() migrationPlanJSON {
	value.t.Helper()
	code, stdout, stderr := value.run("migration", "plan", "--json")
	if code != 0 {
		value.t.Fatalf("migration plan code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var plan migrationPlanJSON
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		value.t.Fatal(err)
	}
	return plan
}

func TestMigrationPlanDoesNotCallHarmlessChangesDataLoss(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		change func(*evolutionSchema)
	}{
		{name: "rename checked column", change: func(schema *evolutionSchema) { schema.titleColumn = "headline" }},
		{name: "rename unchecked column", change: func(schema *evolutionSchema) { schema.bodyColumn = "content" }},
		{name: "widen string", change: func(schema *evolutionSchema) { schema.titleType = "varchar(500)" }},
		{name: "make optional", change: func(schema *evolutionSchema) { schema.titleOptional = true }},
		{name: "rename table", change: func(schema *evolutionSchema) { schema.authorsTable = "writers" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := newEvolution(t, defaultEvolutionSchema())
			changed := value.schema
			testCase.change(&changed)
			value.write(changed)
			plan := value.plan()
			var reviewedTypeChanges []string
			for _, warning := range plan.Warnings {
				if warning == "DATA_LOSS" {
					t.Fatalf("plan warnings=%v", plan.Warnings)
				}
			}
			for _, provider := range plan.Providers {
				for _, phase := range provider.Phases {
					for _, operation := range phase.Operations {
						if operation.Risk == "dataLoss" {
							t.Fatalf("%s %s is labelled dataLoss", provider.Provider, operation.Kind)
						}
						if operation.Approval.Required && operation.Kind != "alterColumnType" {
							t.Fatalf("%s %s demands an approval", provider.Provider, operation.Kind)
						}
						if operation.Approval.Required {
							reviewedTypeChanges = append(reviewedTypeChanges, operation.ID)
						}
					}
				}
			}
			if code, stdout, stderr := value.newMigration("harmless", reviewedTypeChanges); code != 0 {
				t.Fatalf("migration new code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			for _, provider := range value.providers() {
				if code, stdout, stderr := value.apply(provider); code != 0 {
					t.Fatalf("apply %s code=%d stdout=%s stderr=%s", provider, code, stdout, stderr)
				}
				value.assertSchemaCurrent(provider)
			}
			value.assertRowsKept(changed.authorsTable, changed.authorIDColumn)
		})
	}
}

func TestMigrationNewReportsEveryMissingApprovalAtOnce(t *testing.T) {
	value := newEvolution(t, defaultEvolutionSchema())
	dropped := value.schema
	dropped.noteFields = []string{"ID", "AuthorID", "Author"}
	value.write(dropped)
	required := value.requiredApprovals()
	if len(required) < 2 {
		t.Fatalf("dropping two fields requires approvals %v", required)
	}
	code, stdout, stderr := value.newMigration("drop_note_text", nil)
	if code == 0 {
		t.Fatalf("migration new without approvals succeeded: %s", stdout)
	}
	for _, operation := range required {
		if !strings.Contains(stderr, "--approve "+operation) {
			t.Fatalf("missing approval %s is not reported:\n%s", operation, stderr)
		}
	}
}

func TestMigrationNewExplainsARequiredFieldWithoutADefault(t *testing.T) {
	for _, providers := range []string{"golem.SQLite", "golem.SQLite, golem.PostgreSQL"} {
		t.Run(providers, func(t *testing.T) {
			initial := defaultEvolutionSchema()
			initial.providers = providers
			value := newEvolution(t, initial)
			added := value.schema
			added.noteFields = []string{"ID", "AuthorID", "Title", "Body", "Slug", "Author"}
			value.write(added)
			code, stdout, stderr := value.newMigration("add_note_slug", nil)
			if code == 0 {
				t.Fatalf("migration new succeeded: %s", stdout)
			}
			if strings.Contains(stderr, "exactly one PostgreSQL provider") || !strings.Contains(stderr, "notes.slug") || !strings.Contains(stderr, "default") || !strings.Contains(stderr, "optional") {
				t.Fatalf("required field refusal does not explain itself:\n%s", stderr)
			}
		})
	}
}

func TestMigrationPlanNamesAMissingArtifactByItsModulePath(t *testing.T) {
	module := writeSocialModule(t, false)
	createInitialReviewedMigration(t, module)
	artifact := filepath.Join("migrations", "sqlite", "0001_initial.sql")
	if err := os.Remove(filepath.Join(module, artifact)); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), module, []string{"migration", "plan"}, &stdout, &stderr); code != 1 {
		t.Fatalf("migration plan code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	message := stderr.String()
	if strings.Contains(message, "<path>") || strings.Contains(message, "<module>") || strings.Contains(message, module) || strings.Count(message, filepath.ToSlash(artifact)) != 2 {
		t.Fatalf("missing artifact message = %s", message)
	}
}
