package compile

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/gentest"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestContractRenameNeverProducesApplicationMigration(t *testing.T) {
	before := gentest.SocialCompilationIR()
	after := before
	after.Contract.Models = append([]ir.ModelContractIR(nil), before.Contract.Models...)
	for index := range after.Contract.Models {
		if !after.Contract.Models[index].Subscriptions {
			continue
		}
		after.Contract.Models[index].GraphQLName = "Article"
		after.Contract.Models[index].GraphQLPlural = "Articles"
		after.Contract.Models[index].Roots.Events = "articleEvents"
	}
	leftContract, err := ir.ContractFingerprint(before.Contract)
	if err != nil {
		t.Fatal(err)
	}
	rightContract, err := ir.ContractFingerprint(after.Contract)
	if err != nil {
		t.Fatal(err)
	}
	leftModel, err := ir.ModelFingerprint(before.Model)
	if err != nil {
		t.Fatal(err)
	}
	rightModel, err := ir.ModelFingerprint(after.Model)
	if err != nil {
		t.Fatal(err)
	}
	if leftContract == rightContract || leftModel != rightModel {
		t.Fatalf("contract rename fingerprints contract=%s/%s model=%s/%s", leftContract, rightContract, leftModel, rightModel)
	}

	providers := []struct {
		name      string
		namespace physical.PhysicalName
		lower     func(context.Context, ir.ModelIR, physical.LowerOptions) (physical.PhysicalSchema, error)
	}{
		{name: "sqlite", namespace: "main", lower: sqlite.New().Lower},
		{name: "postgresql", namespace: "p7_contract_rename", lower: postgresql.New().Lower},
	}
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			left, err := provider.lower(context.Background(), before.Model, physical.LowerOptions{Namespace: provider.namespace})
			if err != nil {
				t.Fatal(err)
			}
			right, err := provider.lower(context.Background(), after.Model, physical.LowerOptions{Namespace: provider.namespace})
			if err != nil {
				t.Fatal(err)
			}
			leftPhysical, _ := physical.PhysicalFingerprint(left)
			rightPhysical, _ := physical.PhysicalFingerprint(right)
			plan, err := migration.Diff(left, right)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range plan.Operations {
				if operation.Kind != migration.RecordSchemaVersion {
					t.Fatalf("GraphQL-only event rename emitted application migration %#v", operation)
				}
			}
			if leftPhysical != rightPhysical {
				t.Fatalf("GraphQL-only event rename changed physical fingerprint %s/%s", leftPhysical, rightPhysical)
			}
		})
	}
}

func TestSubscriptionToggleChangesContractNotModelOrApplicationDDL(t *testing.T) {
	after := gentest.SocialCompilationIR()
	before := after
	before.Contract.Models = append([]ir.ModelContractIR(nil), after.Contract.Models...)
	for index := range before.Contract.Models {
		if before.Contract.Models[index].Subscriptions {
			before.Contract.Models[index].Subscriptions = false
			before.Contract.Models[index].Roots.Events = ""
			before.Contract.Models[index].Event = nil
		}
	}
	beforeContract, _ := ir.ContractFingerprint(before.Contract)
	afterContract, _ := ir.ContractFingerprint(after.Contract)
	beforeModel, _ := ir.ModelFingerprint(before.Model)
	afterModel, _ := ir.ModelFingerprint(after.Model)
	if beforeContract == afterContract || beforeModel != afterModel {
		t.Fatalf("subscription toggle fingerprint domains contract=%s/%s model=%s/%s", beforeContract, afterContract, beforeModel, afterModel)
	}
	providers := []struct {
		name      string
		namespace physical.PhysicalName
		lower     func(context.Context, ir.ModelIR, physical.LowerOptions) (physical.PhysicalSchema, error)
	}{
		{name: "sqlite", namespace: "main", lower: sqlite.New().Lower},
		{name: "postgresql", namespace: "p7_subscription_toggle", lower: postgresql.New().Lower},
	}
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			left, err := provider.lower(context.Background(), before.Model, physical.LowerOptions{Namespace: provider.namespace})
			if err != nil {
				t.Fatal(err)
			}
			right, err := provider.lower(context.Background(), after.Model, physical.LowerOptions{Namespace: provider.namespace})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := migration.Diff(left, right)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range plan.Operations {
				if operation.Kind != migration.RecordSchemaVersion {
					t.Fatalf("subscription toggle emitted application DDL %#v", operation)
				}
			}
		})
	}
}
