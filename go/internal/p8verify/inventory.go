// Package p8verify owns the independent P8-I local release-candidate audit.
// Local success is useful implementation evidence but cannot complete row 24;
// public-tag installation and the hosted matrix remain separately pending.
package p8verify

import (
	"errors"
	"reflect"
)

const FormatVersion uint16 = 1

type Inventory struct {
	FormatVersion       uint16
	Status              string
	FormalEvidence      string
	Domains             []string
	ProviderProfiles    []string
	RequiredTests       []string
	RegisteredCommands  []string
	PendingIntegrations []string
	Prohibitions        []string
}

func ReleaseAuditInventory() Inventory {
	return Inventory{
		FormatVersion:  FormatVersion,
		Status:         "local-candidate-audit",
		FormalEvidence: "PENDING",
		Domains: []string{
			"artifacts",
			"compatibility",
			"conformance",
			"disclosure",
			"documentation",
			"package-hygiene",
			"providers",
			"recovery",
			"resources",
		},
		ProviderProfiles: []string{"postgresql-c", "postgresql-linguistic", "sqlite"},
		RequiredTests: []string{
			"TestP8IndependentPublicPackageAndArtifactAudit",
			"TestP8IndependentReleaseOraclePostgreSQLProfiles",
			"TestP8IndependentReleaseOracleSQLite",
		},
		RegisteredCommands: []string{
			"go run ./internal/cmd/p8compat -module .",
			"go run ./internal/cmd/p8docs -module .",
			"go run ./internal/cmd/p8verify -module .",
		},
		PendingIntegrations: []string{
			"hosted-full-matrix-structured-events",
			"hosted-mutation-catalog-report",
			"hosted-public-tag-installation",
		},
		Prohibitions: []string{
			"no-formal-pass-from-local-results",
			"no-production-expectation-helper",
			"no-provider-profile-skip",
			"no-repository-workspace-in-external-consumer",
		},
	}
}

func ValidateInventory(value Inventory) error {
	expected := ReleaseAuditInventory()
	if value.FormatVersion != FormatVersion || value.Status != "local-candidate-audit" || value.FormalEvidence != "PENDING" {
		return errors.New("P8_VERIFY_INVENTORY_STATUS")
	}
	if !reflect.DeepEqual(value.Domains, expected.Domains) ||
		!reflect.DeepEqual(value.ProviderProfiles, expected.ProviderProfiles) ||
		!reflect.DeepEqual(value.RequiredTests, expected.RequiredTests) ||
		!reflect.DeepEqual(value.RegisteredCommands, expected.RegisteredCommands) ||
		!reflect.DeepEqual(value.PendingIntegrations, expected.PendingIntegrations) ||
		!reflect.DeepEqual(value.Prohibitions, expected.Prohibitions) {
		return errors.New("P8_VERIFY_INVENTORY_INCOMPLETE")
	}
	return nil
}
