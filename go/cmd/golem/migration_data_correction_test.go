package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestMigrationDataCorrectionCommandLifecycle(t *testing.T) {
	schema := defaultEvolutionSchema()
	schema.providers = "golem.SQLite"
	value := &evolution{t: t, module: t.TempDir(), schema: schema, databases: map[ir.Provider]string{}}
	value.writeModule(schema)
	source := schema.source()
	const marker = `golem.SchemaName(schema, "evolution")`
	if strings.Count(source, marker) != 1 {
		t.Fatal("identity fixture schema marker changed")
	}
	source = strings.Replace(source, marker, marker+"\n\tgolem.WithDataIncarnation(schema)", 1)
	if err := os.WriteFile(filepath.Join(value.module, "schema.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	value.createDatabases()
	if code, stdout, stderr := value.newMigration("initial", value.requiredApprovals()); code != 0 {
		t.Fatalf("initial history: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if code, stdout, stderr := value.apply(ir.SQLite); code != 0 {
		t.Fatalf("initial apply: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	value.exec(`INSERT INTO "authors"(id,handle) VALUES('` + evolutionAuthorID + `','owner')`)
	value.exec(`INSERT INTO "notes"(id,author_id,title,body) VALUES('` + evolutionNoteID + `','` + evolutionAuthorID + `','old','kept')`)
	oldIdentity := value.query(ir.SQLite, `SELECT value FROM _golem_data_incarnation`)
	file := filepath.Join(value.module, "correction.sql")
	if err := os.WriteFile(file, []byte("UPDATE notes SET title='corrected';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"migration", "data", "new", "--name", "correct_title", "--field", "Note.Title", "--file", file}
	code, stdout, stderr := value.run(arguments...)
	if code != 1 || stdout != "" {
		t.Fatalf("unapproved correction: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var approval string
	words := strings.Fields(stderr)
	for index, word := range words {
		if word == "--approve" && index+1 < len(words) {
			approval = words[index+1]
			break
		}
	}
	if approval == "" {
		t.Fatalf("missing actionable approval ID: %s", stderr)
	}
	if code, stdout, stderr := value.run(append(arguments, "--approve", approval)...); code != 0 {
		t.Fatalf("correction authoring: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if code, stdout, stderr := value.run("migration", "plan", "--migration", "0002_correct_title", "--json"); code != 0 || !strings.Contains(stdout, `"correctDataColumn"`) {
		t.Fatalf("reviewed correction plan: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if code, stdout, stderr := value.apply(ir.SQLite); code != 0 {
		t.Fatalf("correction apply: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if got := value.query(ir.SQLite, `SELECT title||':'||body FROM notes`); got != "corrected:kept" {
		t.Fatalf("incorrect correction result: %s", got)
	}
	newIdentity := value.query(ir.SQLite, `SELECT value FROM _golem_data_incarnation`)
	if newIdentity == oldIdentity {
		t.Fatal("CLI correction retained the old data identity")
	}
	if code, stdout, stderr := value.apply(ir.SQLite); code != 0 {
		t.Fatalf("already applied history: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if value.query(ir.SQLite, `SELECT value FROM _golem_data_incarnation`) != newIdentity {
		t.Fatal("CLI retry rotated an already applied identity")
	}
	for _, command := range []string{"generate", "check"} {
		if code, stdout, stderr := value.run(command, "--app-out", "./app"); code != 0 {
			t.Fatalf("%s after correction: code=%d stdout=%s stderr=%s", command, code, stdout, stderr)
		}
	}
	value.assertSchemaCurrent(ir.SQLite)
}
