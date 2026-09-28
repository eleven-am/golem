package drift

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestBetweenNamesFirstChangedCatalogObject(t *testing.T) {
	base := physical.PhysicalSchema{
		Provider:  physical.ProviderManifest{Provider: ir.PostgreSQL},
		Namespace: physical.Namespace{Name: "public"},
		Tables: []physical.PhysicalTable{{
			ID:          "users-model",
			Name:        "users",
			Columns:     []physical.PhysicalColumn{{ID: "id-field", Name: "id"}},
			ForeignKeys: []physical.PhysicalForeignKey{{ID: "team-fk", Name: "users_team_fk"}},
			Indexes:     []physical.PhysicalIndex{{ID: "email-index", Name: "users_email_idx"}},
		}},
		System: physical.SystemSchema{Objects: []physical.SystemObject{{ID: "ledger-object", Name: "_golem_migrations"}}},
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*physical.PhysicalSchema)
		want   Object
	}{
		{
			name: "column",
			mutate: func(schema *physical.PhysicalSchema) {
				schema.Tables[0].Columns = append(schema.Tables[0].Columns, physical.PhysicalColumn{ID: "extra-field", Name: "extra"})
			},
			want: Object{Type: "column", Name: "extra", Table: "users"},
		},
		{
			name: "foreign key",
			mutate: func(schema *physical.PhysicalSchema) {
				schema.Tables[0].ForeignKeys[0].OnDelete = ir.ActionCascade
			},
			want: Object{Type: "foreign_key", Name: "users_team_fk", Table: "users"},
		},
		{
			name: "index",
			mutate: func(schema *physical.PhysicalSchema) {
				schema.Tables[0].Indexes[0].Unique = true
			},
			want: Object{Type: "index", Name: "users_email_idx", Table: "users"},
		},
		{
			name: "system object",
			mutate: func(schema *physical.PhysicalSchema) {
				schema.System.Objects = nil
			},
			want: Object{Type: "table", Name: "_golem_migrations", Table: "_golem_migrations"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			actual := base
			actual.Tables = append([]physical.PhysicalTable(nil), base.Tables...)
			actual.Tables[0].Columns = append([]physical.PhysicalColumn(nil), base.Tables[0].Columns...)
			actual.Tables[0].ForeignKeys = append([]physical.PhysicalForeignKey(nil), base.Tables[0].ForeignKeys...)
			actual.Tables[0].Indexes = append([]physical.PhysicalIndex(nil), base.Tables[0].Indexes...)
			actual.System.Objects = append([]physical.SystemObject(nil), base.System.Objects...)
			testCase.mutate(&actual)
			got, ok := Between(base, actual)
			if !ok || got != testCase.want {
				t.Fatalf("Between()=(%#v,%t), want (%#v,true)", got, ok, testCase.want)
			}
		})
	}
}
