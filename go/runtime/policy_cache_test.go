package runtime

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/observe"
)

func TestPolicyCacheKeysAnEntryByTheEpochCurrentAtInsertion(t *testing.T) {
	cache, err := newPolicyCache(PolicyCacheConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	cache.now = func() time.Time {
		if calls.Add(1) == 1 {
			cache.epoch.Add(1)
		}
		return time.Unix(1_700_000_000, 0)
	}
	builds := 0
	build := func() (*policyruntime.Set, error) {
		builds++
		return &policyruntime.Set{}, nil
	}
	first, err := cache.get("actor", build)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.get("actor", build)
	if err != nil || second != first || builds != 1 {
		t.Fatalf("an entry was stored under a stale epoch: builds=%d shared=%t err=%v", builds, second == first, err)
	}
}

func TestPolicyCacheBuildPanicIsRaisedAndNeverCached(t *testing.T) {
	cache, err := newPolicyCache(PolicyCacheConfig{})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = cache.get("actor", func() (*policyruntime.Set, error) {
			close(entered)
			<-release
			panic("policy set build exploded")
		})
	}()
	<-entered
	type outcome struct {
		set *policyruntime.Set
		err error
	}
	waiter := make(chan outcome, 1)
	go func() {
		set, err := cache.get("actor", func() (*policyruntime.Set, error) { return &policyruntime.Set{}, nil })
		waiter <- outcome{set: set, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if value := <-recovered; value != "policy set build exploded" {
		t.Fatalf("build panic was not re-raised: %v", value)
	}
	waited := <-waiter
	if waited.err == nil && waited.set == nil {
		t.Fatal("a caller waiting on a panicked build received a nil policy set without an error")
	}
	if waited.err != nil && !strings.Contains(waited.err.Error(), "policy set build exploded") {
		t.Fatalf("waiting caller error=%v", waited.err)
	}
	set, err := cache.get("actor", func() (*policyruntime.Set, error) { return &policyruntime.Set{}, nil })
	if err != nil || set == nil || cache.len() != 1 {
		t.Fatalf("a panicked build stayed cached: set=%v err=%v entries=%d", set, err, cache.len())
	}
}

func TestPolicyCacheFailedPolicyBuildSuppressesEventsAsPolicyUnavailable(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("model hub did not subscribe to the transport")
	}
	users := golem.GeneratedPolicyBinding[p7EventActor, p7EventUser](fixture.schema.User, func(p7EventActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[p7EventUser]()
		rules.CanRead(golem.All[p7EventUser]())
		return rules.Freeze(fixture.schema.User)
	})
	broken := golem.GeneratedPolicyBinding[p7EventActor, p7EventPost](fixture.schema.Post, func(p7EventActor) (golem.FrozenPolicy, error) {
		return golem.FrozenPolicy{}, errors.New("post factory refused")
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[p7EventActor]{users, broken}, nil))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app.bindings = bindings
	fixture.app.InvalidatePolicies()
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 2300, "visible", false))
	select {
	case observation := <-fixture.suppressed:
		if observation.Reason() != observe.ReasonRevalidation {
			t.Fatalf("failed policy build suppression reason=%q", observation.Reason())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an event for a model whose policy build failed was not suppressed")
	}
}

type policyCacheActor struct {
	ID    int
	Allow bool
	Score float64
	Tag   any
}

type policyCacheFixture[A any] struct {
	app      *App[A, A]
	builds   *atomic.Int64
	resolves *atomic.Int64
	request  golem.FrozenReadRequest
	user     policyir.ModelID
	fail     *atomic.Bool
}

func newPolicyCacheFixture[A any](t testing.TB, allow func(A) bool, configure func(*Config[A, A])) policyCacheFixture[A] {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "policy-cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	builds := &atomic.Int64{}
	userBinding := golem.GeneratedPolicyBinding[A, testUser](fixture.User, func(actor A) (golem.FrozenPolicy, error) {
		builds.Add(1)
		rules := golem.NewRules[testUser]()
		if allow(actor) {
			rules.CanRead(golem.All[testUser]())
		} else {
			rules.CanRead(golem.None[testUser]())
		}
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[A, testPost](fixture.Post, func(A) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(fixture.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[A]{userBinding, postBinding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	resolves := &atomic.Int64{}
	fail := &atomic.Bool{}
	config := Config[A, A]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal A) (A, error) {
			resolves.Add(1)
			if fail.Load() {
				var zero A
				return zero, errors.New("principal revoked")
			}
			return principal, nil
		},
	}
	if configure != nil {
		configure(&config)
	}
	app, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	request, err := golem.FreezeFindMany(userDescriptor, golem.Select[testUser](golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)))
	if err != nil {
		t.Fatal(err)
	}
	return policyCacheFixture[A]{app: app, builds: builds, resolves: resolves, request: request, user: policyir.ModelID(fixture.User), fail: fail}
}

func (fixture policyCacheFixture[A]) caller(t testing.TB, actor A) *Caller[A, A] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Prepare(fixture.request); err != nil {
		t.Fatal(err)
	}
	return caller
}

func (fixture policyCacheFixture[A]) readAllowed(t testing.TB, caller *Caller[A, A]) bool {
	t.Helper()
	policy, ok := caller.policies.Policy(fixture.user)
	if !ok {
		t.Fatal("caller has no user policy")
	}
	constraint, err := resolve.RowConstraint(policy, policyir.ActionRead, fixture.user)
	if err != nil {
		t.Fatal(err)
	}
	truth, constant := constraint.Constant()
	if !constant {
		t.Fatal("user read constraint is not constant")
	}
	return truth
}

func policyCacheAllow(actor policyCacheActor) bool { return actor.Allow }

func TestPolicyCacheEqualActorsShareOneFactoryBuild(t *testing.T) {
	fixture := newPolicyCacheFixture(t, policyCacheAllow, nil)
	const callers = 16
	first := fixture.caller(t, policyCacheActor{ID: 1, Allow: true})
	for range callers - 1 {
		caller := fixture.caller(t, policyCacheActor{ID: 1, Allow: true})
		if caller.policies != first.policies || caller.execution == first.execution {
			t.Fatal("equal actors did not share one policy set or shared an execution identity")
		}
	}
	if fixture.builds.Load() != 1 || fixture.resolves.Load() != callers {
		t.Fatalf("factory builds=%d resolves=%d want 1 and %d", fixture.builds.Load(), fixture.resolves.Load(), callers)
	}
}

func TestPolicyCacheKeepsDifferentActorsIsolated(t *testing.T) {
	fixture := newPolicyCacheFixture(t, policyCacheAllow, nil)
	const goroutines = 32
	var wait sync.WaitGroup
	wait.Add(goroutines)
	for index := range goroutines {
		go func(index int) {
			defer wait.Done()
			allow := index%2 == 0
			for range 8 {
				caller, err := fixture.app.ForPrincipal(context.Background(), policyCacheActor{ID: 7, Allow: allow})
				if err != nil {
					t.Error(err)
					return
				}
				policy, ok := caller.policies.Policy(fixture.user)
				if !ok {
					t.Error("caller has no user policy")
					return
				}
				constraint, err := resolve.RowConstraint(policy, policyir.ActionRead, fixture.user)
				if err != nil {
					t.Error(err)
					return
				}
				if truth, constant := constraint.Constant(); !constant || truth != allow {
					t.Errorf("actor allow=%t leaked policy truth=%t constant=%t", allow, truth, constant)
					return
				}
			}
		}(index)
	}
	wait.Wait()
	if fixture.builds.Load() != 2 {
		t.Fatalf("factory builds=%d want one per distinct actor", fixture.builds.Load())
	}
}

func TestPolicyCacheResolvesPrincipalOnEveryForPrincipal(t *testing.T) {
	fixture := newPolicyCacheFixture(t, policyCacheAllow, nil)
	actor := policyCacheActor{ID: 3, Allow: true}
	fixture.caller(t, actor)
	fixture.caller(t, actor)
	fixture.fail.Store(true)
	_, err := fixture.app.ForPrincipal(context.Background(), actor)
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeUnauthenticated {
		t.Fatalf("revoked principal with a warm cache error=%v", err)
	}
	fixture.fail.Store(false)
	fixture.caller(t, actor)
	if fixture.resolves.Load() != 4 || fixture.builds.Load() != 1 {
		t.Fatalf("resolves=%d builds=%d want 4 and 1", fixture.resolves.Load(), fixture.builds.Load())
	}
}

func TestPolicyCacheEventDeliveryResolvesEveryEventAndSuppressesRevocation(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("model hub did not subscribe to the transport")
	}
	resolvesBefore := fixture.resolves.Load()
	const delivered = 3
	for index := range delivered {
		id := uint16(2100 + index)
		fixture.publish(t, fixture.notice(t, golem.EventUpdated, id, "visible", false))
		if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(id) {
			t.Fatalf("event %d id=%x", index, event.validated.Metadata().EventID())
		}
	}
	if resolved := fixture.resolves.Load() - resolvesBefore; resolved != delivered {
		t.Fatalf("events resolved the principal %d times want %d", resolved, delivered)
	}
	if fixture.builds.Load() != 1 {
		t.Fatalf("event delivery rebuilt the post policy: builds=%d", fixture.builds.Load())
	}
	fixture.app.resolvePrincipal = func(context.Context, p7EventPrincipal) (p7EventActor, error) {
		return p7EventActor{}, errors.New("principal revoked")
	}
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 2200, "visible", false))
	receiveContext, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_, receiveErr := stream.Recv(receiveContext)
	if code, ok := events.CodeOf(receiveErr); !ok || code != events.CodeSubscriptionRevalidation {
		t.Fatalf("revoked principal with a warm cache code=%q error=%v", code, receiveErr)
	}
}

func TestPolicyCacheKeyDistinguishesFloatBitsAndInterfaceTypes(t *testing.T) {
	key := func(actor policyCacheActor) string {
		t.Helper()
		value, ok := policyCacheKey(actor, nil, nil)
		if !ok {
			t.Fatalf("immutable actor %#v produced no key", actor)
		}
		return value
	}
	nan := math.NaN()
	if key(policyCacheActor{Score: nan}) != key(policyCacheActor{Score: nan}) {
		t.Fatal("identical NaN bits produced different keys")
	}
	if key(policyCacheActor{Score: math.Copysign(0, 1)}) == key(policyCacheActor{Score: math.Copysign(0, -1)}) {
		t.Fatal("+0 and -0 produced one key")
	}
	if key(policyCacheActor{Score: nan}) == key(policyCacheActor{Score: math.Float64frombits(math.Float64bits(nan) ^ 1)}) {
		t.Fatal("distinct NaN payloads produced one key")
	}
	distinct := []any{nil, int32(1), int64(1), uint8(1), float32(1), "1", true, [1]int{1}, struct{ V int }{1}, any(policyCacheActor{})}
	seen := map[string]int{}
	for index, tag := range distinct {
		value := key(policyCacheActor{Tag: tag})
		if previous, ok := seen[value]; ok {
			t.Fatalf("interface values %#v and %#v produced one key", distinct[previous], tag)
		}
		seen[value] = index
	}
	type pair struct{ Left, Right string }
	left, _ := policyCacheKey(pair{"ab", "c"}, nil, nil)
	right, _ := policyCacheKey(pair{"a", "bc"}, nil, nil)
	if left == right {
		t.Fatal("string boundaries were not encoded")
	}
	var anyActor any = 1
	var otherActor any = int8(1)
	anyKey, ok := policyCacheKey(anyActor, nil, nil)
	otherKey, otherOK := policyCacheKey(otherActor, nil, nil)
	if !ok || !otherOK || anyKey == otherKey {
		t.Fatal("interface actor dynamic types produced one key")
	}

	fixture := newPolicyCacheFixture(t, policyCacheAllow, nil)
	fixture.caller(t, policyCacheActor{Allow: true, Score: math.Copysign(0, 1)})
	fixture.caller(t, policyCacheActor{Allow: true, Score: math.Copysign(0, -1)})
	fixture.caller(t, policyCacheActor{Allow: true, Score: nan})
	fixture.caller(t, policyCacheActor{Allow: true, Score: nan})
	fixture.caller(t, policyCacheActor{Allow: true, Tag: int32(1)})
	fixture.caller(t, policyCacheActor{Allow: true, Tag: int64(1)})
	if fixture.builds.Load() != 5 {
		t.Fatalf("factory builds=%d want 5", fixture.builds.Load())
	}
}

func TestPolicyCacheSnapshotActorRequiresAnApplicationKey(t *testing.T) {
	copyActor := func(actor *policyCacheActor) (*policyCacheActor, error) {
		value := *actor
		return &value, nil
	}
	allow := func(actor *policyCacheActor) bool { return actor.Allow }
	uncached := newPolicyCacheFixture(t, allow, func(config *Config[*policyCacheActor, *policyCacheActor]) {
		config.SnapshotActor = copyActor
	})
	actor := &policyCacheActor{ID: 1, Allow: true}
	for range 3 {
		uncached.caller(t, actor)
	}
	if uncached.builds.Load() != 3 {
		t.Fatalf("SnapshotActor without PolicyCacheKey cached: builds=%d", uncached.builds.Load())
	}

	keyed := newPolicyCacheFixture(t, allow, func(config *Config[*policyCacheActor, *policyCacheActor]) {
		config.SnapshotActor = copyActor
		config.PolicyCacheKey = func(actor *policyCacheActor) (string, bool) {
			if actor.ID == 0 {
				return "", false
			}
			return strconv.Itoa(actor.ID) + "/" + strconv.FormatBool(actor.Allow), true
		}
	})
	for range 3 {
		keyed.caller(t, &policyCacheActor{ID: 1, Allow: true})
	}
	denied := keyed.caller(t, &policyCacheActor{ID: 1, Allow: false})
	if keyed.readAllowed(t, denied) {
		t.Fatal("a different application key shared another actor's policy set")
	}
	for range 2 {
		keyed.caller(t, &policyCacheActor{ID: 0, Allow: true})
	}
	if keyed.builds.Load() != 4 {
		t.Fatalf("PolicyCacheKey builds=%d want 4", keyed.builds.Load())
	}
}

func TestPolicyCacheInvalidateAndTTLRebuild(t *testing.T) {
	fixture := newPolicyCacheFixture(t, policyCacheAllow, func(config *Config[policyCacheActor, policyCacheActor]) {
		config.PolicyCache.TTL = time.Minute
	})
	var clock atomic.Int64
	start := time.Unix(1_700_000_000, 0)
	fixture.app.policyCache.now = func() time.Time { return start.Add(time.Duration(clock.Load())) }
	actor := policyCacheActor{ID: 5, Allow: true}
	before := fixture.caller(t, actor)
	fixture.caller(t, actor)
	fixture.app.InvalidatePolicies()
	after := fixture.caller(t, actor)
	if fixture.builds.Load() != 2 || after.policies == before.policies {
		t.Fatalf("InvalidatePolicies did not rebuild: builds=%d", fixture.builds.Load())
	}
	clock.Store(int64(59 * time.Second))
	fixture.caller(t, actor)
	if fixture.builds.Load() != 2 {
		t.Fatalf("an entry younger than its TTL was rebuilt: builds=%d", fixture.builds.Load())
	}
	clock.Store(int64(61 * time.Second))
	fixture.caller(t, actor)
	if fixture.builds.Load() != 3 {
		t.Fatalf("an expired entry was not rebuilt: builds=%d", fixture.builds.Load())
	}
	var absent *App[policyCacheActor, policyCacheActor]
	absent.InvalidatePolicies()
}

func TestPolicyCacheLRUBoundAndDisabled(t *testing.T) {
	fixture := newPolicyCacheFixture(t, policyCacheAllow, func(config *Config[policyCacheActor, policyCacheActor]) {
		config.PolicyCache.MaxActors = 4
	})
	for id := range 10 {
		fixture.caller(t, policyCacheActor{ID: id, Allow: true})
	}
	if fixture.app.policyCache.len() != 4 || fixture.builds.Load() != 10 {
		t.Fatalf("cache entries=%d builds=%d want 4 and 10", fixture.app.policyCache.len(), fixture.builds.Load())
	}
	fixture.caller(t, policyCacheActor{ID: 9, Allow: true})
	if fixture.builds.Load() != 10 {
		t.Fatalf("a recent actor was evicted: builds=%d", fixture.builds.Load())
	}
	fixture.caller(t, policyCacheActor{ID: 0, Allow: true})
	if fixture.builds.Load() != 11 || fixture.app.policyCache.len() != 4 {
		t.Fatalf("the oldest actor was kept or the bound grew: builds=%d entries=%d", fixture.builds.Load(), fixture.app.policyCache.len())
	}

	disabled := newPolicyCacheFixture(t, policyCacheAllow, func(config *Config[policyCacheActor, policyCacheActor]) {
		config.PolicyCache.Disabled = true
	})
	for range 3 {
		disabled.caller(t, policyCacheActor{ID: 1, Allow: true})
	}
	if disabled.builds.Load() != 3 || disabled.app.policyCache != nil {
		t.Fatalf("a disabled cache stored policy sets: builds=%d", disabled.builds.Load())
	}
	disabled.app.InvalidatePolicies()
}

func TestPolicyCacheRejectsNegativeConfiguration(t *testing.T) {
	for _, config := range []PolicyCacheConfig{{MaxActors: -1}, {TTL: -time.Second}} {
		if _, err := newPolicyCache(config); err == nil || !strings.Contains(err.Error(), "P3_RUNTIME_CONFIG") {
			t.Fatalf("config %#v error=%v", config, err)
		}
	}
	cache, err := newPolicyCache(PolicyCacheConfig{})
	if err != nil || cache.limit != defaultPolicyCacheMaxActors || cache.ttl != 0 {
		t.Fatalf("default cache=%#v error=%v", cache, err)
	}
}
