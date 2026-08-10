// Package postgresql implements the PostgreSQL 15+ physical-schema provider.
// Every identifier and expression accepted by this package originates in the
// validated physical schema; no API accepts caller-authored SQL.
package postgresql

import (
	"context"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

const (
	CapabilityJSONB            ir.CapabilityID = "postgresql.jsonb"
	CapabilityGeneratedColumns ir.CapabilityID = "postgresql.generated_columns"
	CapabilityAdvisoryLocks    ir.CapabilityID = "postgresql.advisory_xact_lock"
	CapabilityAnalyticsExact   ir.CapabilityID = "analytics.exact-arithmetic.v1"
)

type Provider struct{}

func New() *Provider { return &Provider{} }

func (*Provider) Manifest() physical.ProviderManifest {
	return physical.PostgreSQLManifest(
		physical.CapabilityFact{ID: CapabilityJSONB, Version: 1, Verification: physical.VerificationVersionFloor},
		physical.CapabilityFact{ID: CapabilityGeneratedColumns, Version: 1, Verification: physical.VerificationVersionFloor},
		physical.CapabilityFact{ID: CapabilityAdvisoryLocks, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: capabilityAdvancedIndexes, Version: 1, Verification: physical.VerificationVersionFloor},
		physical.CapabilityFact{ID: CapabilityPolicyBinaryText, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityPolicyASCIIInsensitive, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityPolicyExactJSON, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityPolicyScalarListJSON, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityPolicyRelationCorrelation, Version: 1, Verification: physical.VerificationRuntimeProbe},
		physical.CapabilityFact{ID: CapabilityAnalyticsExact, Version: 1, Verification: physical.VerificationRuntimeProbe},
	)
}

type CapabilityReport struct {
	Version             physical.Version
	JSONB               bool
	GeneratedColumns    bool
	AdvisoryLocks       bool
	BinaryText          bool
	ASCIIInsensitive    bool
	ExactJSON           bool
	ScalarListJSON      bool
	RelationCorrelation bool
	AnalyticsExact      bool
	SessionSettings     bool
}

func (provider *Provider) Open(ctx context.Context, dataSourceName string) (*sqlx.DB, CapabilityReport, error) {
	return provider.open(ctx, dataSourceName)
}

func (provider *Provider) Lower(ctx context.Context, model ir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	return provider.lower(ctx, model, options)
}

// Script is immutable provider-generated DDL. Its statement list is not
// exported, so application input cannot be smuggled into migration execution.
type Script struct{ statements []string }

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
