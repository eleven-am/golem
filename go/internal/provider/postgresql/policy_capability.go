package postgresql

import (
	"context"
	"fmt"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/jmoiron/sqlx"
)

// PolicyCapabilityProof converts measurements from this concrete opened server
// into the immutable proof consumed by the shared policy compiler. The schema
// fingerprint prevents a proof from being reused with a different descriptor
// registry.
func (*Provider) policyCapabilityProof(fingerprint [32]byte, report CapabilityReport) (policysql.CapabilityProof, error) {
	if report.Version.Major < 15 || !report.JSONB || !report.BinaryText || !report.ASCIIInsensitive || !report.ExactJSON || !report.ScalarListJSON || !report.RelationCorrelation || !report.AnalyticsExact {
		return policysql.CapabilityProof{}, fmt.Errorf("postgresql policy capability proof: incomplete runtime measurements")
	}
	return policysql.NewCapabilityProof(
		policyir.ProviderPostgreSQL,
		fingerprint,
		policyir.CapabilityBinaryText,
		policyir.CapabilityASCIIInsensitiveText,
		policyir.CapabilityExactJSON,
		policyir.CapabilityScalarListJSON,
		policyir.CapabilityRelationCorrelation,
	)
}

func (provider *Provider) PolicyCapabilityProof(ctx context.Context, database *sqlx.DB, fingerprint [32]byte) (policysql.CapabilityProof, error) {
	if database == nil || fingerprint == ([32]byte{}) {
		return policysql.CapabilityProof{}, fmt.Errorf("postgresql policy capability proof: database and schema fingerprint are required")
	}
	report, err := probeCapabilities(ctx, database)
	if err != nil {
		return policysql.CapabilityProof{}, err
	}
	return provider.policyCapabilityProof(fingerprint, report)
}
