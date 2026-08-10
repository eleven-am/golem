package nested

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestEmptyBatchWorkRemainsPresentAcrossClone(t *testing.T) {
	model := policyir.ModelID{15: 1}
	work, err := NewBatchWork(model, nil, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]RuntimeWork{"original": work, "clone": work.clone()} {
		rows, present := candidate.BatchRows()
		if !present || len(rows) != 0 || candidate.TouchedRows() != 0 {
			t.Fatalf("%s empty batch present=%t rows=%d touched=%d", name, present, len(rows), candidate.TouchedRows())
		}
	}
}

func TestExecuteRunsElevenShapeGraphDepthFirstAndVerifiesReverse(t *testing.T) {
	fixture := schematest.New(t)
	built, err := Build(Request{Root: systemRoot(t, fixture), Mutations: allNestedMutations(t, fixture), Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 5, MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	transaction := &recordingNestedTransaction{registry: fixture.Registry}
	receipt, err := Execute(context.Background(), built.Graph(), 64, recordingNestedBoundary{transaction})
	if err != nil {
		t.Fatal(err)
	}
	if !transaction.committed || transaction.rolledBack {
		t.Fatal("successful nested graph did not commit exactly once")
	}
	if receipt.TouchedRows() != 15 || len(receipt.Applied()) != 15 {
		t.Fatalf("touched/applied=%d/%d want 15", receipt.TouchedRows(), len(receipt.Applied()))
	}
	wantOperations := map[mutationir.Operation]bool{
		mutationir.Create: true, mutationir.CreateMany: true, mutationir.Connect: true, mutationir.ConnectOrCreate: true,
		mutationir.Disconnect: true, mutationir.SetRelation: true, mutationir.Update: true, mutationir.UpdateMany: true,
		mutationir.Upsert: true, mutationir.Delete: true, mutationir.DeleteMany: true,
	}
	for _, operation := range transaction.expanded {
		delete(wantOperations, operation)
	}
	if len(wantOperations) != 0 {
		t.Fatalf("operations never expanded: %#v", wantOperations)
	}
	if len(transaction.applied) != len(transaction.verified) {
		t.Fatalf("applied/verified=%d/%d", len(transaction.applied), len(transaction.verified))
	}
	for index := range transaction.applied {
		if !sameAppliedRecord(transaction.verified[index], transaction.applied[len(transaction.applied)-1-index]) {
			t.Fatalf("verification is not reverse depth-first at %d", index)
		}
	}
	for index := 1; index < len(transaction.applied); index++ {
		previous, current := transaction.applied[index-1], transaction.applied[index]
		if previous.ordinal == current.ordinal && bytes.Compare(previous.key, current.key) >= 0 {
			t.Fatalf("dynamic work for node %d is not in canonical order", current.ordinal)
		}
	}
}

func TestExecuteRollsBackDenialAtDepthAndTouchedOverflow(t *testing.T) {
	fixture := schematest.New(t)
	built, err := Build(Request{Root: systemRoot(t, fixture), Mutations: allNestedMutations(t, fixture), Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 5, MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	denied := &recordingNestedTransaction{registry: fixture.Registry, denyApply: 7}
	if _, err := Execute(context.Background(), built.Graph(), 64, recordingNestedBoundary{denied}); !errors.Is(err, errNestedDenial) {
		t.Fatalf("denial error=%v", err)
	}
	if denied.committed || !denied.rolledBack || len(denied.verified) != 0 {
		t.Fatal("denied nested graph did not roll back before verification")
	}
	overflow := &recordingNestedTransaction{registry: fixture.Registry}
	if _, err := Execute(context.Background(), built.Graph(), 4, recordingNestedBoundary{overflow}); err == nil {
		t.Fatal("total touched-row overflow succeeded")
	}
	if overflow.committed || !overflow.rolledBack || len(overflow.applied) != 4 {
		t.Fatalf("overflow transaction state commit=%v rollback=%v writes=%d", overflow.committed, overflow.rolledBack, len(overflow.applied))
	}
}

func TestOptionalSourceDeleteExpandsCapturedTargetOnceBeforeDisconnect(t *testing.T) {
	fixture := schematest.NewSubscribedIndexedOptionalSource(t)
	mutations := freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post,
		golem.GeneratedNestedDelete[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, nil)))
	built, err := Build(Request{Root: rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID), Mutations: mutations, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	transaction := &recordingNestedTransaction{registry: fixture.Registry}
	receipt, err := Execute(context.Background(), built.Graph(), 8, recordingNestedBoundary{transaction})
	if err != nil {
		t.Fatal(err)
	}
	if want := []mutationir.Operation{mutationir.Update, mutationir.Delete, mutationir.Disconnect}; !reflect.DeepEqual(transaction.expanded, want) {
		t.Fatalf("expansion order=%v want=%v; delete must expand once before disconnect", transaction.expanded, want)
	}
	var applied []mutationir.Operation
	for _, value := range receipt.Applied() {
		applied = append(applied, value.Node().Operation())
	}
	if want := []mutationir.Operation{mutationir.Update, mutationir.Disconnect, mutationir.Delete}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("apply order=%v want=%v", applied, want)
	}
}

func TestSourceConnectOrCreateExecutesOnlyChosenOwnerEffect(t *testing.T) {
	fixture := schematest.New(t)
	userTarget := golem.GeneratedUniqueSelectorValue[nestedUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, golem.NewUUID([16]byte{2})))
	create := golem.GeneratedCreateInput[nestedUser](fixture.User,
		golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedEqualField[nestedUser, golem.UUID](fixture.UserID), golem.NewUUID([16]byte{3})),
		golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedTextField[nestedUser, string](fixture.UserName), "created"),
	)
	mutations := freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post,
		golem.GeneratedNestedConnectOrCreate[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, userTarget, create),
	))
	built, err := Build(Request{Root: rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID), Mutations: mutations, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		branch     mutationir.Branch
		operations []mutationir.Operation
		parent     uint32
	}{
		{name: "connect", branch: mutationir.ConnectOrCreateConnectBranch, operations: []mutationir.Operation{mutationir.Update, mutationir.Connect}, parent: 2},
		{name: "create", branch: mutationir.ConnectOrCreateCreateBranch, operations: []mutationir.Operation{mutationir.Update, mutationir.Create, mutationir.Connect}, parent: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction := &recordingNestedTransaction{registry: fixture.Registry, connectOrCreateBranch: test.branch}
			receipt, executeErr := Execute(context.Background(), built.Graph(), 8, recordingNestedBoundary{transaction})
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			var operations []mutationir.Operation
			for _, applied := range receipt.Applied() {
				operations = append(operations, applied.Node().Operation())
			}
			if !reflect.DeepEqual(operations, test.operations) {
				t.Fatalf("chosen branch operations=%v", operations)
			}
			effect := receipt.Applied()[len(receipt.Applied())-1]
			anchor, ok := effect.Node().RelationAnchorOrdinal()
			if !ok || anchor != 0 {
				t.Fatal("source owner effect lost its original Post anchor")
			}
			parent, ok := transaction.effectParent[effect.Node().Ordinal()]
			if !ok || parent != test.parent {
				t.Fatalf("owner effect did not consume truthful branch result: parent=%d", parent)
			}
		})
	}
}

var errNestedDenial = errors.New("injected nested denial")

type recordingNestedBoundary struct{ transaction *recordingNestedTransaction }

func (boundary recordingNestedBoundary) BeginNested(context.Context) (ExecutionTransaction, error) {
	return boundary.transaction, nil
}

type appliedRecord struct {
	ordinal uint32
	key     []byte
}

func sameAppliedRecord(left, right appliedRecord) bool {
	return left.ordinal == right.ordinal && bytes.Equal(left.key, right.key)
}

type recordingNestedTransaction struct {
	registry              *schema.Registry
	connectOrCreateBranch mutationir.Branch
	denyApply             int
	expanded              []mutationir.Operation
	applied               []appliedRecord
	verified              []appliedRecord
	effectParent          map[uint32]uint32
	committed             bool
	rolledBack            bool
}

func (transaction *recordingNestedTransaction) ExpandNested(_ context.Context, request ExpansionRequest) (RuntimeExpansion, error) {
	node := request.Node()
	transaction.expanded = append(transaction.expanded, node.Operation())
	switch node.Operation() {
	case mutationir.ConnectOrCreate:
		branch := transaction.connectOrCreateBranch
		if branch == 0 {
			branch = mutationir.ConnectOrCreateConnectBranch
		}
		return NewRuntimeExpansion(nil, branch)
	case mutationir.Upsert:
		return NewRuntimeExpansion(nil, mutationir.UpsertUpdateBranch)
	case mutationir.CreateMany:
		return NewRuntimeExpansion(nil, 0)
	}
	count := 1
	if node.Operation() == mutationir.UpdateMany || node.Operation() == mutationir.DeleteMany {
		count = 2
	}
	works := make([]RuntimeWork, count)
	for index := range works {
		key := []byte{byte('b' - index)} // deliberately reversed; engine canonicalizes.
		if node.Operation() == mutationir.Create {
			works[index], _ = NewCreateWork(node.ModelID(), key)
			continue
		}
		identity := testRuntimeIdentity(transaction.registry, node.ModelID(), byte(node.Ordinal())+byte(index)+1)
		works[index], _ = NewExistingWork(node.ModelID(), identity, key)
	}
	return NewRuntimeExpansion(works, 0)
}

func (transaction *recordingNestedTransaction) ApplyNested(_ context.Context, request ApplyRequest) (ApplyResult, error) {
	if transaction.denyApply != 0 && len(transaction.applied)+1 == transaction.denyApply {
		return ApplyResult{}, errNestedDenial
	}
	node, work := request.Node(), request.Work()
	transaction.applied = append(transaction.applied, appliedRecord{ordinal: node.Ordinal(), key: work.OrderKey()})
	if node.Operation() == mutationir.Connect {
		if parent, ok := request.Parent(); ok {
			if transaction.effectParent == nil {
				transaction.effectParent = make(map[uint32]uint32)
			}
			transaction.effectParent[node.Ordinal()] = parent.Node().Ordinal()
		}
	}
	row := testRuntimeRow(transaction.registry, node.ModelID(), byte(node.Ordinal())+1)
	switch node.Operation() {
	case mutationir.Create, mutationir.BranchProbe:
		return NewApplyResult(nil, &row), nil
	case mutationir.Delete, mutationir.DeleteMany:
		return NewApplyResult(&row, nil), nil
	default:
		return NewApplyResult(&row, &row), nil
	}
}

func (transaction *recordingNestedTransaction) VerifyNested(_ context.Context, applied AppliedNode) error {
	transaction.verified = append(transaction.verified, appliedRecord{ordinal: applied.Node().Ordinal(), key: applied.Work().OrderKey()})
	return nil
}
func (transaction *recordingNestedTransaction) CommitNested(context.Context) error {
	transaction.committed = true
	return nil
}
func (transaction *recordingNestedTransaction) RollbackNested(context.Context) error {
	transaction.rolledBack = true
	return nil
}

func testRuntimeIdentity(registry *schema.Registry, modelID policyir.ModelID, seed byte) mutationdecode.Identity {
	model, _ := registry.Model(golem.ModelID(modelID))
	var primary schema.Identity
	for _, identity := range model.Identities() {
		if identity.Kind() == compilerir.KeyPrimary {
			primary = identity
			break
		}
	}
	components := make([]mutationdecode.IdentityComponent, len(primary.Fields()))
	for index, field := range primary.Fields() {
		components[index], _ = mutationdecode.IdentityValue(policyir.FieldID(field), policyir.UUIDValue([16]byte{15: seed + byte(index)}))
	}
	identity, err := mutationdecode.NewIdentity(primary.KeyID(), components)
	if err != nil {
		panic(err)
	}
	return identity
}

func testRuntimeRow(registry *schema.Registry, model policyir.ModelID, seed byte) mutationdecode.Row {
	identity := testRuntimeIdentity(registry, model, seed)
	cells := make([]mutationdecode.Cell, len(identity.Components()))
	for index, component := range identity.Components() {
		value, _ := component.PolicyValue()
		cells[index] = mutationdecode.Value(component.FieldID(), value)
	}
	row, err := mutationdecode.NewRow(registry, model, cells)
	if err != nil {
		panic(fmt.Sprintf("test row: %v", err))
	}
	return row
}
