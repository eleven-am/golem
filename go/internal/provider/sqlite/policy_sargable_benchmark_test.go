package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

func BenchmarkSQLiteSargableReads200K(b *testing.B) {
	ctx := context.Background()
	database, _, err := New().Open(ctx, filepath.Join(b.TempDir(), "sargable-200k.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE probe (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, body TEXT NOT NULL) STRICT`); err != nil {
		b.Fatal(err)
	}
	transaction, err := database.Begin()
	if err != nil {
		b.Fatal(err)
	}
	insert, err := transaction.Prepare(`INSERT INTO probe(id,name,body) VALUES (?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	body := strings.Repeat("x", 2*1024)
	for id := int64(1); id <= 200_000; id++ {
		if _, err := insert.Exec(id, fmt.Sprintf("row-%06d", id), body); err != nil {
			b.Fatal(err)
		}
	}
	if err := insert.Close(); err != nil {
		b.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		b.Fatal(err)
	}
	if _, err := database.Exec(`ANALYZE`); err != nil {
		b.Fatal(err)
	}
	resolver := newSQLitePolicyTestResolver(b)
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
	if err != nil {
		b.Fatal(err)
	}
	compile := func(field ir.FieldID, typ ir.TypeRef, operatorID ir.OperatorID, value ir.Value) policysql.Fragment {
		operand, err := ir.OneOperand(value)
		if err != nil {
			b.Fatal(err)
		}
		requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionScalar, FieldType: typ, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
		if err != nil {
			b.Fatal(err)
		}
		condition, err := ir.NewScalar(resolver.modelID, field, typ, operatorID, ir.ComparisonSensitive, operand, requirements)
		if err != nil {
			b.Fatal(err)
		}
		fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
		if err != nil {
			b.Fatal(err)
		}
		return fragment
	}
	integer := func(value int64) ir.Value {
		result, err := ir.SignedValue(ir.ValueInt64, value)
		if err != nil {
			b.Fatal(err)
		}
		return result
	}
	text := func(value string) ir.Value {
		result, err := ir.StringValue(value)
		if err != nil {
			b.Fatal(err)
		}
		return result
	}

	primary := compile(resolver.idID, resolver.intType, ir.OperatorEqual, integer(150_000))
	unique := compile(resolver.nameID, resolver.textType, ir.OperatorEqual, text("row-150000"))
	for _, benchmark := range []struct {
		name     string
		fragment policysql.Fragment
	}{
		{name: "FindUnique/primary_key", fragment: primary},
		{name: "FindUnique/unique_field", fragment: unique},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			statement := `SELECT "root"."id" FROM "probe" AS "root" WHERE ` + benchmark.fragment.SQL()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var id int64
				if err := database.GetContext(ctx, &id, statement, benchmark.fragment.Args()...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	for _, matches := range []int64{20, 200, 2_000} {
		fragment := compile(resolver.idID, resolver.intType, ir.OperatorLessThanOrEqual, integer(matches))
		b.Run(fmt.Sprintf("FindMany/indexed_range/matches_%d", matches), func(b *testing.B) {
			statement := `SELECT "root"."id" FROM "probe" AS "root" WHERE ` + fragment.SQL() + ` ORDER BY "root"."id"`
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				rows := make([]int64, 0, matches)
				if err := database.SelectContext(ctx, &rows, statement, fragment.Args()...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
