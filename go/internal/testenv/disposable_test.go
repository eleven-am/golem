package testenv

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestDisposableDatabaseReplacementCoversURLAndKeywordDataSourceNames(t *testing.T) {
	for _, shape := range []struct{ name, base, want string }{
		{
			name: "url",
			base: "postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable",
			want: "postgresql://postgres@127.0.0.1:55433/replacement?sslmode=disable",
		},
		{
			name: "keyword value",
			base: "host=127.0.0.1 port=55433 dbname=golem user=postgres",
			want: "host=127.0.0.1 port=55433 dbname=golem user=postgres dbname=replacement",
		},
		{
			name: "keyword value with percent",
			base: "host=127.0.0.1 dbname=golem password=100%sure",
			want: "host=127.0.0.1 dbname=golem password=100%sure dbname=replacement",
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			if replaced := replaceDisposableDatabase(shape.base, "replacement"); replaced != shape.want {
				t.Fatalf("replacement=%q want=%q", replaced, shape.want)
			}
		})
	}
}

func TestDisposablePostgreSQLIsEmptyAndRemovedAfterTheTest(t *testing.T) {
	var name string
	t.Run("provisioned", func(t *testing.T) {
		dsn := DisposablePostgreSQL(t, PostgreSQLDSNVariable)
		database, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		ctx := context.Background()
		if err := database.QueryRowContext(ctx, `SELECT current_database()`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(name, "golem_disposable_") {
			t.Fatalf("disposable database name=%q", name)
		}
		var schemas int
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.schemata WHERE schema_name IN ('_golem','public') AND schema_name='_golem'`).Scan(&schemas); err != nil {
			t.Fatal(err)
		}
		if schemas != 0 {
			t.Fatalf("disposable database already carries the system schema: %d", schemas)
		}
	})
	if name == "" {
		t.Skip("disposable database was not provisioned")
	}
	admin, err := sql.Open("pgx", PostgreSQLDSN(t, PostgreSQLDSNVariable))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var remaining int
	if err := admin.QueryRowContext(context.Background(), `SELECT count(*) FROM pg_database WHERE datname=$1`, name).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("disposable database %s survived its test", name)
	}
}

func TestDisposablePostgreSQLKeepsTheTemplateLocale(t *testing.T) {
	for _, variable := range []string{PostgreSQLDSNVariable, LinguisticDSNVariable} {
		t.Run(variable, func(t *testing.T) {
			base := PostgreSQLDSN(t, variable)
			admin, err := sql.Open("pgx", base)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			var collate, characterType string
			if err := admin.QueryRowContext(context.Background(), `SELECT datcollate,datctype FROM pg_database WHERE datname=current_database()`).Scan(&collate, &characterType); err != nil {
				t.Fatal(err)
			}
			database, err := sql.Open("pgx", DisposablePostgreSQL(t, variable))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			var disposableCollate, disposableCharacterType string
			if err := database.QueryRowContext(context.Background(), `SELECT datcollate,datctype FROM pg_database WHERE datname=current_database()`).Scan(&disposableCollate, &disposableCharacterType); err != nil {
				t.Fatal(err)
			}
			if disposableCollate != collate || disposableCharacterType != characterType {
				t.Fatalf("disposable locale collate=%q ctype=%q want collate=%q ctype=%q", disposableCollate, disposableCharacterType, collate, characterType)
			}
		})
	}
}

func TestDisposableCreateStatementCarriesTheSourceLocale(t *testing.T) {
	statement := disposableCreateStatement("golem_disposable_1_2", "UTF8", "en_US.utf8", "en_US.utf8")
	for _, fragment := range []string{
		`CREATE DATABASE "golem_disposable_1_2"`,
		"TEMPLATE template0",
		"ENCODING 'UTF8'",
		"LC_COLLATE 'en_US.utf8'",
		"LC_CTYPE 'en_US.utf8'",
	} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("create statement %q omits %q", statement, fragment)
		}
	}
	if quoted := disposableCreateStatement("d", "UTF8", "it's", "C"); !strings.Contains(quoted, "LC_COLLATE 'it''s'") {
		t.Fatalf("create statement did not escape the locale literal: %q", quoted)
	}
}
