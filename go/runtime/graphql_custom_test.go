package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	golemgraphql "github.com/eleven-am/golem/go/graphql"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

type customTransactionArgs struct {
	ID   golem.UUID
	Fail bool
}

func TestCustomMutationTransactionCommitsAndRollsBackExactlyOnceWithoutReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(caller).(golemgraphql.CustomCallerCapability); !ok {
		t.Fatal("runtime Caller lacks custom GraphQL caller capability")
	}
	if _, ok := any(fixture.app.System()).(golemgraphql.CustomCallerCapability); ok {
		t.Fatal("System acquired custom GraphQL caller capability")
	}
	if _, ok := any(&CallerTx[mutationResultPrincipal, mutationResultActor]{}).(golemgraphql.CustomCallerCapability); ok {
		t.Fatal("CallerTx acquired custom GraphQL caller capability")
	}
	compiled := compile.Compile(ctx, compile.Config{Dir: "../internal/compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	var operation compilerir.CustomOperationContractIR
	for _, candidate := range compilation.Contract.CustomOperations {
		if candidate.Operation == compilerir.CustomOperationMutation {
			operation = candidate
			break
		}
	}
	if operation.ExtensionID == "" {
		t.Fatal("fixture has no custom mutation")
	}
	operation.Name = "transactionProbe"
	operation.Arguments = []compilerir.GraphQLArgumentContractIR{
		{Name: "id", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "UUID"}},
		{Name: "fail", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Boolean"}},
	}
	operation.Result = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: "Boolean"}
	operation.Resolver = compilerir.AttachedMethodIR{PackagePath: compilation.Model.Schema.PackagePath, Name: "TransactionProbe", Kind: "custommutation"}
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{operation}

	invocations, callbacks := 0, 0
	binding, err := golemgraphql.BindCustomMutation[*Caller[mutationResultPrincipal, mutationResultActor], customTransactionArgs, bool](golemgraphql.CustomBindingSpec{
		ExtensionID: string(operation.ExtensionID), ResolverPackage: operation.Resolver.PackagePath, ResolverName: operation.Resolver.Name,
	}, func(arguments []golemgraphql.CustomArgument) (customTransactionArgs, error) {
		id, idOK := golemgraphql.CustomArgumentValue[golem.UUID](arguments, "id")
		fail, failOK := golemgraphql.CustomArgumentValue[bool](arguments, "fail")
		if !idOK || !failOK {
			return customTransactionArgs{}, errors.New("custom transaction arguments are not exact")
		}
		return customTransactionArgs{ID: id, Fail: fail}, nil
	}, func(ctx context.Context, got *Caller[mutationResultPrincipal, mutationResultActor], args customTransactionArgs) (bool, error) {
		invocations++
		if got != caller {
			return false, errors.New("custom mutation received another caller")
		}
		err := CallerTransaction(ctx, got, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			callbacks++
			_, createErr := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(args.ID[15], golem.UUID{15: 1}, "custom-transaction"))
			if createErr != nil {
				return createErr
			}
			if args.Fail {
				return errors.New("explicit custom rollback")
			}
			return nil
		})
		return err == nil, err
	}, func(value bool) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	registry, err := golemgraphql.NewCustomRegistry(customTransactionBundle(t, compilation), binding)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := registry.ForCaller(caller)
	if err != nil {
		t.Fatal(err)
	}
	committedID := golem.UUID{15: 80}
	result, err := execution.Execute(ctx, golemgraphql.CustomMutation, operation.Name, map[string]any{"id": committedID, "fail": false})
	if err != nil || result.Value() != true {
		t.Fatalf("commit result=%#v err=%v", result.Value(), err)
	}
	rolledBackID := golem.UUID{15: 81}
	if _, err := execution.Execute(ctx, golemgraphql.CustomMutation, operation.Name, map[string]any{"id": rolledBackID, "fail": true}); err == nil {
		t.Fatal("explicit custom transaction rollback succeeded")
	}
	if invocations != 2 || callbacks != 2 {
		t.Fatalf("custom transaction replayed: invocations=%d callbacks=%d", invocations, callbacks)
	}
	for _, test := range []struct {
		id    byte
		count int
	}{{80, 1}, {81, 0}} {
		var count int
		if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(test.id)); err != nil || count != test.count {
			t.Fatalf("post %d count=%d want=%d err=%v", test.id, count, test.count, err)
		}
	}
}

func customTransactionBundle(t *testing.T, compilation compilerir.CompilationIR) golem.SchemaBundle {
	t.Helper()
	modelBytes, err := compilerir.CanonicalModel(compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := compilerir.ModelFingerprint(compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	model := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), customTransactionDigest(t, modelFingerprint), modelBytes)
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), customTransactionDigest(t, contractFingerprint), contractBytes)
	return golem.GeneratedSchemaBundle(golem.SchemaDigest{5}, "custom-transaction-test", "custom-transaction-test", model, contract)
}

func customTransactionDigest(t *testing.T, fingerprint compilerir.Fingerprint) (result golem.SchemaDigest) {
	t.Helper()
	decoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(decoded) != len(result) {
		t.Fatalf("fingerprint %q: %v", fingerprint, err)
	}
	copy(result[:], decoded)
	return result
}
