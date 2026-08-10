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
	if report.Version.Major < 15 || !report.JSONB || !report.BinaryText || !report.ASCIIInsensitive || !report.ExactJSON || !report.ScalarListJSON || !report.RelationCorrelation || !report.AnalyticsExact || !report.SessionSettings {
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

// VerifyPool proves every sealed pool slot while holding the entire bounded
// pool. Any existing borrower or acquisition wait invalidates startup.
func (*Provider) VerifyPool(ctx context.Context, database *sqlx.DB, width int) (CapabilityReport, error) {
	if database == nil || width < 1 || database.Stats().InUse != 0 {
		return CapabilityReport{}, fmt.Errorf("postgresql pool verification requires an idle bounded database")
	}
	waitCount := database.Stats().WaitCount
	connections := make([]*sqlx.Conn, 0, width)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < width; index++ {
		connection, err := database.Connx(ctx)
		if err != nil {
			return CapabilityReport{}, fmt.Errorf("postgresql pool verification connection %d: %w", index, err)
		}
		connections = append(connections, connection)
		if database.Stats().WaitCount != waitCount {
			return CapabilityReport{}, fmt.Errorf("postgresql pool verification observed concurrent pool use")
		}
	}
	if database.Stats().InUse != width {
		return CapabilityReport{}, fmt.Errorf("postgresql pool verification did not exclusively hold every slot")
	}
	var combined CapabilityReport
	for index, connection := range connections {
		report, err := probeCapabilities(ctx, connection)
		if err != nil {
			return CapabilityReport{}, err
		}
		if index == 0 {
			combined = report
			continue
		}
		if combined.Version != report.Version {
			return CapabilityReport{}, fmt.Errorf("postgresql pool verification observed different server versions")
		}
		combined.JSONB = combined.JSONB && report.JSONB
		combined.GeneratedColumns = combined.GeneratedColumns && report.GeneratedColumns
		combined.AdvisoryLocks = combined.AdvisoryLocks && report.AdvisoryLocks
		combined.BinaryText = combined.BinaryText && report.BinaryText
		combined.ASCIIInsensitive = combined.ASCIIInsensitive && report.ASCIIInsensitive
		combined.ExactJSON = combined.ExactJSON && report.ExactJSON
		combined.ScalarListJSON = combined.ScalarListJSON && report.ScalarListJSON
		combined.RelationCorrelation = combined.RelationCorrelation && report.RelationCorrelation
		combined.AnalyticsExact = combined.AnalyticsExact && report.AnalyticsExact
		combined.SessionSettings = combined.SessionSettings && report.SessionSettings
	}
	if database.Stats().WaitCount != waitCount {
		return CapabilityReport{}, fmt.Errorf("postgresql pool verification observed concurrent pool use")
	}
	return combined, nil
}

func (provider *Provider) VerifiedPoolPolicyCapabilityProof(ctx context.Context, database *sqlx.DB, fingerprint [32]byte, width int) (policysql.CapabilityProof, error) {
	if database == nil || fingerprint == ([32]byte{}) {
		return policysql.CapabilityProof{}, fmt.Errorf("postgresql policy capability proof: database and schema fingerprint are required")
	}
	report, err := provider.VerifyPool(ctx, database, width)
	if err != nil {
		return policysql.CapabilityProof{}, err
	}
	return provider.policyCapabilityProof(fingerprint, report)
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
