// Package sqlite implements Golem's SQLite physical-schema provider. All SQL
// originates from validated physical descriptors; no API accepts caller SQL.
package sqlite

import (
	"context"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

const (
	CapabilityJSON1             ir.CapabilityID = "sqlite.json1"
	CapabilityGeneratedColumns  ir.CapabilityID = "sqlite.generated_columns"
	CapabilityExpressionIndexes ir.CapabilityID = "sqlite.expression_indexes"
	CapabilityPartialIndexes    ir.CapabilityID = "sqlite.partial_indexes"
	CapabilityPolicyBinaryText  ir.CapabilityID = "policy.binary-text.v1"
	CapabilityPolicyASCIIText   ir.CapabilityID = "policy.ascii-insensitive-text.v1"
	CapabilityPolicyExactJSON   ir.CapabilityID = "policy.exact-json.v1"
	CapabilityPolicyScalarList  ir.CapabilityID = "scalar-list.json-array.v1"
	CapabilityPolicyRelation    ir.CapabilityID = "policy.relation-correlation.v1"
	CapabilityAnalyticsExact    ir.CapabilityID = "analytics.exact-arithmetic.v1"
	CapabilityVec0              ir.CapabilityID = "sqlite.vec0.v1"
)

type Provider struct{}

func New() *Provider { return &Provider{} }

func (*Provider) Manifest() physical.ProviderManifest {
	return physical.SQLiteManifest(
		physical.CapabilityFact{ID: CapabilityJSON1, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityGeneratedColumns, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityExpressionIndexes, Version: 1, Verification: physical.VerificationVersionFloor},
		physical.CapabilityFact{ID: CapabilityPartialIndexes, Version: 1, Verification: physical.VerificationVersionFloor},
		physical.CapabilityFact{ID: CapabilityPolicyBinaryText, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityPolicyASCIIText, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityPolicyExactJSON, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityPolicyScalarList, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityPolicyRelation, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityAnalyticsExact, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityVec0, Version: 1, Verification: physical.VerificationRuntimeProbe},
	)
}

type CapabilityReport struct {
	Version          physical.Version
	ForeignKeys      bool
	JSON1            bool
	GeneratedColumns bool
	PolicyBinaryText bool
	PolicyASCIIText  bool
	PolicyExactJSON  bool
	PolicyScalarList bool
	PolicyRelation   bool
	AnalyticsExact   bool
	Vec0             bool
	VecVersion       string
}

// Open opens the CGO-free sqlite-vec WASM database through sqlx and proves the provider
// floor and required connection capabilities before returning it.
func (provider *Provider) Open(ctx context.Context, dataSourceName string) (*sqlx.DB, CapabilityReport, error) {
	return provider.open(ctx, dataSourceName)
}

func (provider *Provider) Probe(ctx context.Context, database *sqlx.DB) (CapabilityReport, error) {
	return provider.probe(ctx, database)
}

// Lower implements physical.Lowerer.
func (provider *Provider) Lower(ctx context.Context, model ir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	return provider.lower(ctx, model, options)
}

// Script is immutable provider-generated DDL. It has no public constructor and
// cannot carry application-supplied statements.
type Script struct {
	statements []string
}

func (script Script) SQL() string {
	if len(script.statements) == 0 {
		return ""
	}
	return strings.Join(script.statements, ";\n") + ";\n"
}

func (provider *Provider) RenderInitial(schema physical.PhysicalSchema) (Script, error) {
	return provider.renderInitial(schema)
}

func (provider *Provider) ApplyInitial(ctx context.Context, database *sqlx.DB, schema physical.PhysicalSchema) error {
	return provider.applyInitial(ctx, database, schema)
}

func (provider *Provider) Introspect(ctx context.Context, database *sqlx.DB, expected physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	return provider.introspect(ctx, database, expected)
}

func (provider *Provider) Verify(ctx context.Context, database *sqlx.DB, expected physical.PhysicalSchema) error {
	return provider.verify(ctx, database, expected)
}
