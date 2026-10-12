package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func enumCheckSource(fields []string, checks []string) string {
	declared := map[string]string{
		"Platform":    "Platform Platform `db:\"platform\"`",
		"Status":      "Status Status `db:\"status\"`",
		"FailureCode": "FailureCode golem.Null[string] `db:\"failure_code\"`",
		"Kind":        "Kind Kind `db:\"kind\"`",
	}
	var lines []string
	for _, name := range fields {
		lines = append(lines, "\t"+declared[name])
	}
	var options []string
	for _, check := range checks {
		options = append(options, "\t\t"+check+",")
	}
	return `package enumcheck

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }

type Platform string

const PlatformTwitch Platform = "twitch"

func (Platform) GolemEnum() golem.EnumSpec[Platform] {
	return golem.DefineEnum(golem.EnumValue(PlatformTwitch))
}

type Status string

const (
	StatusQueued Status = "queued"
	StatusFailed Status = "failed"
)

func (Status) GolemEnum() golem.EnumSpec[Status] {
	return golem.DefineEnum(golem.EnumValue(StatusQueued), golem.EnumValue(StatusFailed))
}

type Kind string

const (
	KindAlpha Kind = "alpha"
	KindBeta  Kind = "beta"
)

func (Kind) GolemEnum() golem.EnumSpec[Kind] {
	return golem.DefineEnum(golem.EnumValue(KindAlpha), golem.EnumValue(KindBeta))
}

type Job struct {
	_ struct{} ` + "`golem:\"model;id=enumcheck.Job;table=jobs\"`" + `
	ID golem.UUID ` + "`db:\"id\" golem:\"id=enumcheck.Job.ID;pk;default=uuid\"`" + `
` + strings.Join(lines, "\n") + `
}

func (Job) GolemModel() golem.ModelSpec[Job] {
	return golem.DefineModel[Job](
` + strings.Join(options, "\n") + `
	)
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "enumcheck")
	golem.Actor[Actor](schema)
	golem.Model[Job](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (Job) DefinePolicy(rules *golem.Rules[Job], actor Actor) {
	_ = rules
	_ = actor
}
`
}

func newEnumCheckEvolution(t *testing.T, fields []string, checks []string) *evolution {
	t.Helper()
	module := t.TempDir()
	goMod := "module example.test/enumcheck\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(commandModuleRoot(t)) + "\n"
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "schema.go"), []byte(enumCheckSource(fields, checks)), 0o644); err != nil {
		t.Fatal(err)
	}
	value := &evolution{t: t, module: module, schema: evolutionSchema{providers: "golem.SQLite, golem.PostgreSQL"}, databases: map[ir.Provider]string{}}
	if code, stdout, stderr := value.newMigration("initial", value.requiredApprovals()); code != 0 {
		t.Fatalf("migration new initial code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	value.createDatabases()
	for _, provider := range value.providers() {
		if code, stdout, stderr := value.apply(provider); code != 0 {
			t.Fatalf("migration apply initial on %s code=%d stdout=%s stderr=%s", provider, code, stdout, stderr)
		}
		value.assertSchemaCurrent(provider)
	}
	return value
}

func (value *evolution) replacePostgreSQLCheck(column, definition string) {
	value.t.Helper()
	name := value.query(ir.PostgreSQL, `SELECT conname FROM pg_catalog.pg_constraint WHERE conrelid='"public"."jobs"'::regclass AND contype='c' AND (conname='`+column+`' OR pg_catalog.pg_get_constraintdef(oid) LIKE '%`+column+`%') ORDER BY conname LIMIT 1`)
	database, err := openDoctorDatabasePublic(value.t.Context(), ir.PostgreSQL, value.dataSource(ir.PostgreSQL))
	if err != nil {
		value.t.Fatal(err)
	}
	defer database.Close()
	statement := `ALTER TABLE "public"."jobs" DROP CONSTRAINT "` + name + `", ADD CONSTRAINT "` + name + `" CHECK (` + definition + `)`
	if _, err := database.UnsafeSQLX().Exec(statement); err != nil {
		value.t.Fatalf("%s: %v", statement, err)
	}
}

func (value *evolution) assertPostgreSQLSchemaDrift() {
	value.t.Helper()
	_, stdout, stderr := value.run("doctor", "--provider", string(ir.PostgreSQL), "--dsn", value.databases[ir.PostgreSQL], "--json")
	var output doctorOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		value.t.Fatalf("doctor output=%s stderr=%s: %v", stdout, stderr, err)
	}
	if output.History != "current" || output.Schema == "current" {
		value.t.Fatalf("altered enum check was not reported as drift: history=%s schema=%s diagnostics=%#v", output.History, output.Schema, output.Diagnostics)
	}
}

func TestMigrationApplyVerifiesEnumChecksOnEveryProvider(t *testing.T) {
	t.Run("single value enum", func(t *testing.T) {
		value := newEnumCheckEvolution(t, []string{"Platform"}, nil)
		value.replacePostgreSQLCheck("platform", `"platform" IN ('kick')`)
		value.assertPostgreSQLSchemaDrift()
	})
	t.Run("check compares an enum column to an enum value", func(t *testing.T) {
		value := newEnumCheckEvolution(t, []string{"Status"}, []string{`golem.Check("ck_jobs_failed", Jobs.Status.Expr().Eq(StatusFailed))`})
		value.replacePostgreSQLCheck("ck_jobs_failed", `"status" = 'queued'`)
		value.assertPostgreSQLSchemaDrift()
	})
	t.Run("check combines an enum comparison with another column", func(t *testing.T) {
		value := newEnumCheckEvolution(t, []string{"Status", "FailureCode"}, []string{`golem.Check("ck_jobs_failure_code", Jobs.Status.Expr().Ne(StatusFailed).Or(Jobs.FailureCode.Expr().IsNotNull()))`})
		value.replacePostgreSQLCheck("ck_jobs_failure_code", `"status" <> 'queued' OR "failure_code" IS NOT NULL`)
		value.assertPostgreSQLSchemaDrift()
	})
	t.Run("two value enum", func(t *testing.T) {
		value := newEnumCheckEvolution(t, []string{"Kind"}, nil)
		value.replacePostgreSQLCheck("kind", `"kind" = 'alpha'`)
		value.assertPostgreSQLSchemaDrift()
	})
}
