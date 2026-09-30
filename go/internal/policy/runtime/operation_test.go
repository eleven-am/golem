package runtime

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/gentest"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

type runtimeOperationArguments struct{}

func runtimeInviteOperation(context.Context, *runtimeUser, runtimeOperationArguments) (bool, error) {
	return true, nil
}

func runtimeRenameOperation(context.Context, *runtimeUser, runtimeOperationArguments) (bool, error) {
	return true, nil
}

func runtimeAuditOperation(context.Context, *runtimeUser, runtimeOperationArguments) (bool, error) {
	return true, nil
}

type runtimeOperationFixture struct {
	set    *Set
	user   policyir.ModelID
	handle policyir.FieldID
	id     policyir.FieldID
	invite golem.OperationID
	rename golem.OperationID
	audit  golem.OperationID
}

func newRuntimeOperationFixture(t *testing.T) runtimeOperationFixture {
	t.Helper()
	registry, fixture := runtimeSchemaFixture(t)
	var userID golem.FieldID
	for _, model := range gentest.SocialCompilationIR().Model.Models {
		if model.LogicalName != "User" {
			continue
		}
		for _, field := range model.Fields {
			if field.GoName == "ID" {
				userID = runtimeFieldID(t, field.ID)
			}
		}
	}
	handle := golem.GeneratedTextField[runtimeUser, string](fixture.userHandle)
	identity := golem.GeneratedEqualField[runtimeUser, golem.UUID](userID)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(runtimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(golem.All[runtimeUser]())
		rules.CanUpdate(golem.All[runtimeUser]())
		rules.CannotUpdateFields(golem.All[runtimeUser](), handle, identity)
		golem.Within(rules, runtimeInviteOperation).CanCreate(golem.All[runtimeUser]())
		golem.Within(rules, runtimeRenameOperation).CanUpdateFields(golem.All[runtimeUser](), handle)
		return rules.Freeze(fixture.user)
	})
	bindings, err := golem.GeneratedApplicationOperations(runtimeBindings(t, fixture.generation, binding),
		golem.GeneratedCustomMutationOperation("invite", runtimeInviteOperation),
		golem.GeneratedCustomMutationOperation("rename", runtimeRenameOperation),
		golem.GeneratedCustomMutationOperation("audit", runtimeAuditOperation),
	)
	if err != nil {
		t.Fatal(err)
	}
	set, err := Build(BuildRequest[runtimeActor]{Bindings: bindings, Registry: registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
	if err != nil {
		t.Fatal(err)
	}
	invite, inviteOK := bindings.Operation(runtimeInviteOperation)
	rename, renameOK := bindings.Operation(runtimeRenameOperation)
	audit, auditOK := bindings.Operation(runtimeAuditOperation)
	if !inviteOK || !renameOK || !auditOK {
		t.Fatal("registered operations did not resolve")
	}
	return runtimeOperationFixture{set: set, user: policyir.ModelID(fixture.user), handle: policyir.FieldID(fixture.userHandle), id: policyir.FieldID(userID), invite: invite, rename: rename, audit: audit}
}

func runtimeCreateAllowed(t *testing.T, set *Set, model policyir.ModelID) bool {
	t.Helper()
	policy, ok := set.Policy(model)
	if !ok {
		t.Fatal("policy is absent")
	}
	row, err := resolve.RowConstraint(policy, policyir.ActionCreate, model)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := resolve.ActionAllowed(row)
	if err != nil {
		t.Fatal(err)
	}
	return allowed
}

func runtimeUpdateFieldAllowed(t *testing.T, set *Set, model policyir.ModelID, field policyir.FieldID) bool {
	t.Helper()
	policy, ok := set.Policy(model)
	if !ok {
		t.Fatal("policy is absent")
	}
	condition, err := resolve.FieldCondition(policy, policyir.ActionUpdate, model, field)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := resolve.ActionAllowed(condition)
	if err != nil {
		t.Fatal(err)
	}
	return allowed
}

func TestWithinSetGrantsOnlyItsOwnOperationUntilReleased(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	if runtimeCreateAllowed(t, fixture.set, fixture.user) {
		t.Fatal("the caller policy grants a create that only an operation may perform")
	}
	invite, releaseInvite := fixture.set.Within(fixture.invite)
	rename, releaseRename := fixture.set.Within(fixture.rename)
	defer releaseRename()
	if !runtimeCreateAllowed(t, invite, fixture.user) {
		t.Fatal("the invite operation did not receive its create grant")
	}
	if runtimeCreateAllowed(t, rename, fixture.user) {
		t.Fatal("the rename operation received another operation's create grant")
	}
	if runtimeCreateAllowed(t, fixture.set, fixture.user) {
		t.Fatal("entering an operation widened the caller's own policy")
	}
	releaseInvite()
	if runtimeCreateAllowed(t, invite, fixture.user) {
		t.Fatal("a released operation kept its grant")
	}
	releaseInvite()
	if invite.GenerationDigest() != fixture.set.GenerationDigest() || invite.Provider() != fixture.set.Provider() {
		t.Fatal("an operation set changed generation or provider identity")
	}
}

func TestWithinFieldGrantLeavesCallerDenialsOnUncoveredFields(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	rename, release := fixture.set.Within(fixture.rename)
	defer release()
	if runtimeUpdateFieldAllowed(t, fixture.set, fixture.user, fixture.handle) {
		t.Fatal("the caller policy updates a field it denies")
	}
	if !runtimeUpdateFieldAllowed(t, rename, fixture.user, fixture.handle) {
		t.Fatal("a Within field grant did not override the caller denial on the field it names")
	}
	if runtimeUpdateFieldAllowed(t, rename, fixture.user, fixture.id) {
		t.Fatal("a Within field grant lifted the caller denial on a field it does not name")
	}
	invite, releaseInvite := fixture.set.Within(fixture.invite)
	defer releaseInvite()
	if runtimeUpdateFieldAllowed(t, invite, fixture.user, fixture.handle) {
		t.Fatal("another operation's field grant applied")
	}
}

func TestWithinOnAnOperationSetStartsFromTheCallerPolicy(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	invite, release := fixture.set.Within(fixture.invite)
	defer release()
	nested, releaseNested := invite.Within(fixture.rename)
	defer releaseNested()
	if runtimeCreateAllowed(t, nested, fixture.user) {
		t.Fatal("a nested operation accumulated the enclosing operation's grant")
	}
	if !runtimeUpdateFieldAllowed(t, nested, fixture.user, fixture.handle) {
		t.Fatal("a nested operation lost its own grant")
	}
	releaseNested()
	if !runtimeCreateAllowed(t, invite, fixture.user) {
		t.Fatal("releasing a nested operation closed the enclosing one")
	}
	audit, releaseAudit := invite.Within(fixture.audit)
	defer releaseAudit()
	if runtimeCreateAllowed(t, audit, fixture.user) || runtimeUpdateFieldAllowed(t, audit, fixture.user, fixture.handle) {
		t.Fatal("an operation without grants inherited the enclosing operation's grants")
	}
}

func TestWithinReleaseIsSafeUnderConcurrentLookups(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	invite, release := fixture.set.Within(fixture.invite)
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				if _, ok := invite.Policy(fixture.user); !ok {
					t.Error("policy disappeared during release")
					return
				}
			}
		}()
	}
	release()
	wait.Wait()
	if runtimeCreateAllowed(t, invite, fixture.user) {
		t.Fatal("released operation kept its grant")
	}
}

func TestBuildRejectsWithinGrantForUnregisteredResolverNamingIt(t *testing.T) {
	registry, fixture := runtimeSchemaFixture(t)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(runtimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(golem.All[runtimeUser]())
		golem.Within(rules, runtimeRenameOperation).CanCreate(golem.All[runtimeUser]())
		return rules.Freeze(fixture.user)
	})
	bindings, err := golem.GeneratedApplicationOperations(runtimeBindings(t, fixture.generation, binding), golem.GeneratedCustomMutationOperation("invite", runtimeInviteOperation))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(BuildRequest[runtimeActor]{Bindings: bindings, Registry: registry, Provider: policyir.ProviderSQLite, Capabilities: runtimeProof(t, registry, policyir.ProviderSQLite, allRuntimeCapabilities...)})
	if err == nil || !strings.Contains(err.Error(), "runtimeRenameOperation") {
		t.Fatalf("unregistered Within error=%v", err)
	}
}

func runtimeReleaseIsWaiting() bool {
	buffer := make([]byte, 1<<22)
	stacks := string(buffer[:goruntime.Stack(buffer, true)])
	for _, stack := range strings.Split(stacks, "\n\n") {
		if strings.Contains(stack, "operationScope).release") && strings.Contains(stack, "sync.(*Mutex).Lock") {
			return true
		}
	}
	return false
}

func TestLeaseCommitsOnlyWhileItsOperationIsLive(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	invite, release := fixture.set.Within(fixture.invite)
	unused, unusedLease := invite.Lease()
	used, usedLease := invite.Lease()
	if _, ok := used.Policy(fixture.user); !ok || !usedLease.Used() {
		t.Fatal("a lookup of a model the operation grants on did not use the lease")
	}
	if unusedLease.Used() {
		t.Fatal("a lease was used by another lease's lookup")
	}
	release()
	commits := 0
	if err := unusedLease.Commit(func() error { commits++; return nil }); err != nil || commits != 1 {
		t.Fatalf("a lease that never used a grant was refused after release: %v", err)
	}
	if err := usedLease.Commit(func() error { commits++; return nil }); !errors.Is(err, ErrOperationEnded) || commits != 1 || !usedLease.Ended() {
		t.Fatalf("a lease that used a grant committed after its operation ended: %v commits=%d", err, commits)
	}
	if _, ok := unused.Policy(fixture.user); !ok || unusedLease.Used() {
		t.Fatal("a lookup after release used the lease")
	}
	if base, _ := fixture.set.Lease(); base != fixture.set {
		t.Fatal("a caller set without operation grants was leased")
	}
	if invite.Base() != fixture.set || fixture.set.Base() != fixture.set {
		t.Fatal("an operation set did not return the caller's own set as its base")
	}
}

func TestLeaseReuseIsLimitedToItsOwnOperation(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	invite, releaseInvite := fixture.set.Within(fixture.invite)
	defer releaseInvite()
	rename, releaseRename := fixture.set.Within(fixture.rename)
	defer releaseRename()
	_, lease := invite.Lease()
	if reused := invite.WithLease(lease); reused == invite {
		t.Fatal("a lease was not reused for its own operation")
	} else if _, ok := reused.Policy(fixture.user); !ok || !lease.Used() {
		t.Fatal("a reused lease did not record its use")
	}
	if rename.WithLease(lease) != rename || fixture.set.WithLease(lease) != fixture.set || invite.WithLease(nil) != invite {
		t.Fatal("a lease was applied outside its own operation")
	}
}

func TestReleaseWaitsForACommitInProgress(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	invite, release := fixture.set.Within(fixture.invite)
	used, lease := invite.Lease()
	if _, ok := used.Policy(fixture.user); !ok {
		t.Fatal("policy is absent")
	}
	released := make(chan struct{})
	committed := false
	err := lease.Commit(func() error {
		go func() {
			release()
			close(released)
		}()
		for !runtimeReleaseIsWaiting() {
			select {
			case <-released:
				t.Error("release completed while a commit it guards was in progress")
				return nil
			default:
				goruntime.Gosched()
			}
		}
		committed = true
		return nil
	})
	<-released
	if err != nil || !committed {
		t.Fatalf("commit in progress = %v committed=%t", err, committed)
	}
	if err := lease.Commit(func() error { return nil }); !errors.Is(err, ErrOperationEnded) {
		t.Fatalf("a commit after release completed = %v", err)
	}
}

func TestCommitLeasesRequiresEveryOperationLive(t *testing.T) {
	fixture := newRuntimeOperationFixture(t)
	invite, releaseInvite := fixture.set.Within(fixture.invite)
	rename, releaseRename := fixture.set.Within(fixture.rename)
	defer releaseInvite()
	first, firstLease := invite.Lease()
	second, secondLease := rename.Lease()
	first.Policy(fixture.user)
	second.Policy(fixture.user)
	commits := 0
	if err := CommitLeases([]*Lease{firstLease, secondLease, nil, firstLease}, func() error { commits++; return nil }); err != nil || commits != 1 {
		t.Fatalf("live leases = %v commits=%d", err, commits)
	}
	releaseRename()
	if err := CommitLeases([]*Lease{firstLease, secondLease}, func() error { commits++; return nil }); !errors.Is(err, ErrOperationEnded) || commits != 1 || !secondLease.Ended() {
		t.Fatalf("a commit with one ended operation = %v commits=%d", err, commits)
	}
}
