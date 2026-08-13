// Package provider exposes provider-verified database ownership shared by
// generated Golem applications. Raw SQL access is deliberately named unsafe.
package provider

import (
	"time"

	"github.com/eleven-am/golem/go/golem"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	"github.com/jmoiron/sqlx"
)

type Feature string

const (
	FeatureForeignKeys      Feature = "foreign-keys.v1"
	FeatureJSON             Feature = "json.v1"
	FeatureGeneratedColumns Feature = "generated-columns.v1"
	FeatureAdvisoryLocks    Feature = "advisory-locks.v1"
	FeaturePolicyBinaryText Feature = "policy-binary-text.v1"
	FeaturePolicyASCIIText  Feature = "policy-ascii-text.v1"
	FeaturePolicyExactJSON  Feature = "policy-exact-json.v1"
	FeaturePolicyScalarList Feature = "policy-scalar-list.v1"
	FeaturePolicyRelation   Feature = "policy-relation.v1"
	FeatureAnalyticsExact   Feature = "analytics-exact.v1"
)

type Version struct {
	Major uint32
	Minor uint32
	Patch uint32
}

type Capabilities struct {
	provider golem.Provider
	version  Version
	features []Feature
}

func (value Capabilities) Provider() golem.Provider { return value.provider }
func (value Capabilities) ServerVersion() Version   { return value.version }
func (value Capabilities) Features() []Feature {
	return append([]Feature(nil), value.features...)
}

type PoolStatus struct {
	maximumOpen               int
	maximumIdle               int
	connectionMaximumLifetime time.Duration
	connectionMaximumIdleTime time.Duration
}

func (value PoolStatus) MaximumOpen() int { return value.maximumOpen }
func (value PoolStatus) MaximumIdle() int { return value.maximumIdle }
func (value PoolStatus) ConnectionMaximumLifetime() time.Duration {
	return value.connectionMaximumLifetime
}
func (value PoolStatus) ConnectionMaximumIdleTime() time.Duration {
	return value.connectionMaximumIdleTime
}

// Database is a provider-opened, capability-proved database. Its fields are
// sealed, so applications cannot manufacture a valid handle around a raw pool.
// Provider subpackages convert only successfully verified internal handles to
// this distinct public type.
type Database providerhandle.Database

func (database *Database) Provider() golem.Provider {
	return (*providerhandle.Database)(database).Provider()
}

func (database *Database) Capabilities() Capabilities {
	internal := (*providerhandle.Database)(database).Capabilities()
	version := internal.ServerVersion()
	features := internal.Features()
	publicFeatures := make([]Feature, len(features))
	for index, feature := range features {
		publicFeatures[index] = Feature(feature)
	}
	return Capabilities{
		provider: internal.Provider(),
		version:  Version{Major: version.Major, Minor: version.Minor, Patch: version.Patch},
		features: publicFeatures,
	}
}

func (database *Database) Pool() PoolStatus {
	internal := (*providerhandle.Database)(database).Pool()
	return PoolStatus{
		maximumOpen:               internal.MaximumOpen(),
		maximumIdle:               internal.MaximumIdle(),
		connectionMaximumLifetime: internal.ConnectionMaximumLifetime(),
		connectionMaximumIdleTime: internal.ConnectionMaximumIdleTime(),
	}
}

// UnsafeSQLX exposes the raw pool only to trusted infrastructure. Work through
// it bypasses every Golem authorization, hook, transaction, invalidation,
// outbox, and event guarantee.
func (database *Database) UnsafeSQLX() *sqlx.DB {
	return (*providerhandle.Database)(database).UnsafeSQLX()
}

func (database *Database) Close() error {
	return (*providerhandle.Database)(database).Close()
}

type Code string

const (
	CodeConfig      Code = "PROVIDER_CONFIG"
	CodeOpen        Code = "PROVIDER_OPEN"
	CodeClose       Code = "PROVIDER_CLOSE"
	CodeMaintenance Code = "PROVIDER_MAINTENANCE"
)

func CodeOf(err error) (Code, bool) {
	code, ok := providerhandle.CodeOf(err)
	if !ok {
		return "", false
	}
	return Code(code), true
}
