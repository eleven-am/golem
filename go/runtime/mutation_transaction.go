package runtime

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/jmoiron/sqlx"
)

// mutationTransactionPlan is resolved before an outer transaction begins.
// Savepoints reuse their outer binding and cannot upgrade its certificate.
// Options are owned values so no caller can change a plan through a pointer.
type mutationTransactionPlan struct {
	options      sql.TxOptions
	hasOptions   bool
	serializable bool
	valid        bool
}

func mutationTransactionDefaultIsolation(provider policyir.Provider, registry *schema.Registry) golem.IsolationLevel {
	if provider == policyir.ProviderPostgreSQL && mutationHistoryWriterRequired(registry) {
		return golem.Serializable
	}
	return golem.ReadCommitted
}

func planMutationTransaction(provider policyir.Provider, registry *schema.Registry, isolation golem.IsolationLevel) (mutationTransactionPlan, error) {
	return resolveMutationTransactionPlan(provider, isolation, mutationHistoryWriterRequired(registry))
}

// resolveMutationTransactionPlan has no retry or writer side effects. A
// required history transaction must be rejected here rather than downgraded
// after application code has read or written through the transaction.
func resolveMutationTransactionPlan(provider policyir.Provider, isolation golem.IsolationLevel, historyRequired bool) (mutationTransactionPlan, error) {
	if provider != policyir.ProviderPostgreSQL && provider != policyir.ProviderSQLite {
		return mutationTransactionPlan{}, fmt.Errorf("P4_RUNTIME_TRANSACTION: transaction provider is unsupported")
	}
	if isolation != golem.ReadCommitted && isolation != golem.Serializable {
		return mutationTransactionPlan{}, fmt.Errorf("P4_RUNTIME_TRANSACTION: transaction isolation is unsupported")
	}
	if provider == policyir.ProviderPostgreSQL && historyRequired && isolation != golem.Serializable {
		return mutationTransactionPlan{}, fmt.Errorf("P5_POLICY_HISTORY: PostgreSQL historical writing requires Serializable transaction isolation")
	}
	plan := mutationTransactionPlan{valid: true}
	if options := transactionBeginOptions(provider, isolation); options != nil {
		plan.options, plan.hasOptions = *options, true
		plan.serializable = provider == policyir.ProviderPostgreSQL && options.Isolation == sql.LevelSerializable
	}
	return plan, nil
}

// beginMutationTransaction is the only issuer of a PostgreSQL history
// snapshot certificate. The binding is created only after the driver accepts
// the resolved isolation. Retry and commit ownership stay with each caller.
func beginMutationTransaction(ctx context.Context, database *sqlx.DB, plan mutationTransactionPlan) (*executionBinding, error) {
	if ctx == nil || database == nil || !plan.valid {
		return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: context, database, and resolved transaction plan are required")
	}
	var options *sql.TxOptions
	if plan.hasOptions {
		copy := plan.options
		options = &copy
	}
	transaction, err := database.BeginTxx(ctx, options)
	if err != nil {
		return nil, err
	}
	binding := transactionExecution(database, transaction)
	binding.historySerializable = plan.serializable
	return binding, nil
}
