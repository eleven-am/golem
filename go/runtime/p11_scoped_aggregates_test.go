package runtime_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/runtime/testdata/p11aggregates"
)

const (
	p11AggregateNamespace       physical.PhysicalName = "golem_p11_aggregates"
	p11AggregateSystemNamespace physical.PhysicalName = "golem_p11_aggregates_system"
)

type p11AggregateLowerer struct{ delegate physical.Lowerer }

func (lowerer p11AggregateLowerer) Manifest() physical.ProviderManifest {
	return lowerer.delegate.Manifest()
}

func (lowerer p11AggregateLowerer) Lower(ctx context.Context, model compilerir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	options.Namespace = p11AggregateNamespace
	schema, err := lowerer.delegate.Lower(ctx, model, options)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	schema.System.Namespace.Name = p11AggregateSystemNamespace
	return physical.Normalize(schema)
}

func p11AggregateGenerationRequest(moduleRoot string) pipeline.Request {
	directory := filepath.Join(moduleRoot, "runtime", "testdata", "p11aggregates")
	return pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/runtime/testdata/p11aggregates", PackageName: "p11aggregates", Directory: directory},
		Lowerers:   []physical.Lowerer{p11AggregateLowerer{delegate: postgresprovider.New()}, sqliteprovider.New()},
	}
}

func TestP11AggregateFixtureRegeneratesByteIdentically(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	result, err := pipeline.Build(context.Background(), p11AggregateGenerationRequest(moduleRoot))
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range p5ExtensionArtifactMap(result) {
		if !strings.HasPrefix(path, "runtime/testdata/p11aggregates/") {
			continue
		}
		committed, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, committed) {
			t.Fatalf("committed aggregates fixture is stale: %s: %s", path, p5ExtensionFirstDifference(committed, content))
		}
	}
}

type p11Fixture struct {
	app       *p11aggregates.App[p11aggregates.Principal]
	artifacts [3]golem.UUID
	collect   [4]golem.UUID
}

type p11CallerTx = p11aggregates.CallerTx[p11aggregates.Principal]

func forEachP11AggregateProfile(t *testing.T, run func(*testing.T, *p11Fixture)) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			p11aggregates.SetProbe(nil)
			t.Cleanup(func() { p11aggregates.SetProbe(nil) })
			run(t, newP11Fixture(t, profile))
		})
	}
}

func newP11Fixture(t *testing.T, profile p5ExtensionProviderProfile) *p11Fixture {
	t.Helper()
	ctx := context.Background()
	database := openP10OperationDatabase(t, profile, &p10CommitGate{})
	apply := sqliteprovider.New().ApplyInitial
	if profile.provider == golem.PostgreSQL {
		apply = postgresprovider.New().ApplyInitial
	}
	var encoded []byte
	for _, document := range p11aggregates.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == profile.provider {
			encoded = document.Schema().Bytes()
		}
	}
	schema, err := physical.CanonicalDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	application, err := p11aggregates.Open(ctx, p11aggregates.Config[p11aggregates.Principal]{
		Database:            p8AdoptTracedProviderHandle(database, profile),
		EventTransport:      transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			t.Errorf("after-commit hook failed: %v", failure.Cause())
		},
		ResolvePrincipal: func(_ context.Context, principal p11aggregates.Principal) (p11aggregates.Actor, error) {
			return p11aggregates.Actor{ID: principal.ID, Misconfigured: principal.Misconfigured, Unregistered: principal.Unregistered}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &p11Fixture{app: application}
	for index := range fixture.artifacts {
		fixture.artifacts[index] = p10OperationID(t, 10+index)
	}
	for index := range fixture.collect {
		fixture.collect[index] = p10OperationID(t, 20+index)
	}
	fixture.seed(t)
	return fixture
}

func (fixture *p11Fixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	system := fixture.app.System()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	A := p11aggregates.Artifacts
	for index, owner := range []string{"alice", "bob", "carol"} {
		manager := "mia"
		if owner == "carol" {
			manager = "carol"
		}
		_, err := system.Artifacts.Create(ctx, A.Create(A.ID.Create(fixture.artifacts[index]), A.Owner.Create(owner), A.Manager.Create(manager), A.Title.Create(owner+"'s artifact")))
		must(err)
	}
	C := p11aggregates.Collections
	for index, collection := range []struct {
		artifact int
		owner    string
	}{{0, "bob"}, {1, "bob"}, {2, "carol"}, {0, "mia"}} {
		_, err := system.Collections.Create(ctx, C.Create(C.ID.Create(fixture.collect[index]), C.ArtifactID.Create(fixture.artifacts[collection.artifact]), C.Owner.Create(collection.owner), C.Name.Create("collection")))
		must(err)
	}
	D := p11aggregates.Documents
	for index, document := range []struct {
		collection int
		owner      string
	}{{0, "bob"}, {0, "bob"}, {1, "bob"}, {2, "carol"}, {0, "mia"}} {
		_, err := system.Documents.Create(ctx, D.Create(D.ID.Create(p10OperationID(t, 30+index)), D.CollectionID.Create(fixture.collect[document.collection]), D.Owner.Create(document.owner), D.Title.Create("document")))
		must(err)
	}
	S := p11aggregates.Assets
	for index, asset := range []struct {
		artifact int
		owner    string
		label    string
		size     int64
	}{{0, "alice", "cover", 100}, {0, "bob", "draft", 250}, {0, "bob", "secret", 7}, {1, "bob", "other", 1000}} {
		_, err := system.Assets.Create(ctx, S.Create(S.ID.Create(p10OperationID(t, 40+index)), S.ArtifactID.Create(fixture.artifacts[asset.artifact]), S.Owner.Create(asset.owner), S.Label.Create(asset.label), S.Size.Create(asset.size)))
		must(err)
	}
	H := p11aggregates.Shares
	for index, share := range []struct {
		artifact int
		grantee  string
	}{{0, "bob"}, {0, "carol"}, {1, "alice"}} {
		_, err := system.Shares.Create(ctx, H.Create(H.ID.Create(p10OperationID(t, 50+index)), H.ArtifactID.Create(fixture.artifacts[share.artifact]), H.Grantee.Create(share.grantee)))
		must(err)
	}
	I := p11aggregates.Invites
	for index, invite := range []struct {
		artifact int
		owner    string
		email    string
		status   string
	}{{0, "alice", "dave", "pending"}, {0, "alice", "erin", "accepted"}, {1, "bob", "alice", "pending"}} {
		_, err := system.Invites.Create(ctx, I.Create(I.ID.Create(p10OperationID(t, 60+index)), I.ArtifactID.Create(fixture.artifacts[invite.artifact]), I.Owner.Create(invite.owner), I.Email.Create(invite.email), I.Status.Create(invite.status)))
		must(err)
	}
}

func (fixture *p11Fixture) caller(t *testing.T, principal p11aggregates.Principal) *p11aggregates.Caller[p11aggregates.Principal] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func p11Run[Args, R any](t *testing.T, caller *p11aggregates.Caller[p11aggregates.Principal], operation func(context.Context, *p11CallerTx, Args) (R, error), arguments Args) (R, error) {
	t.Helper()
	var result R
	var runErr error
	err := caller.Transaction(context.Background(), func(tx *p11CallerTx) error {
		result, runErr = p11aggregates.Run(context.Background(), tx, operation, arguments)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result, runErr
}

func p11Forbidden(t *testing.T, label string, err error) {
	t.Helper()
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeForbidden {
		t.Errorf("%s: err = %v, want FORBIDDEN", label, err)
	}
}

func p11Sum(ctx context.Context, tx *p11CallerTx, where ...golem.Predicate[p11aggregates.Asset]) (int64, error) {
	S := p11aggregates.Assets
	total := S.Size.Sum()
	options := []golem.AggregateOption[p11aggregates.Asset]{S.AggregateSelect(total)}
	for _, predicate := range where {
		options = append(options, S.AggregateWhere(predicate))
	}
	result, err := tx.Assets.Aggregate(ctx, S.Aggregate(options...))
	if err != nil {
		return 0, err
	}
	sum, present := golem.AggregateValue(result, total).Get()
	if !present {
		return 0, nil
	}
	value, _ := sum.Int64()
	return value, nil
}

func p11Expect(t *testing.T, label string, got int64, err error, want int64) {
	t.Helper()
	if err != nil || got != want {
		chain := ""
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			chain += " | " + cause.Error()
		}
		t.Errorf("%s = %d, %s; want %d", label, got, chain, want)
	}
}

func TestScopedCountAndAggregateWidenOnlyInsideRunAcrossProviders(t *testing.T) {
	forEachP11AggregateProfile(t, func(t *testing.T, fixture *p11Fixture) {
		ctx := context.Background()
		alice := fixture.caller(t, p11aggregates.Principal{ID: "alice"})
		mia := fixture.caller(t, p11aggregates.Principal{ID: "mia"})

		budget, err := p11Run(t, alice, p11aggregates.CommitAssetBudget, p11aggregates.BudgetArgs{ArtifactID: fixture.artifacts[0]})
		p11Expect(t, "asset budget across every uploader inside Run", budget, err, 357)
		foreign, err := p11Run(t, alice, p11aggregates.CommitAssetBudget, p11aggregates.BudgetArgs{ArtifactID: fixture.artifacts[1]})
		p11Expect(t, "asset budget of an artifact the grant does not cover", foreign, err, 0)
		capacity, err := p11Run(t, mia, p11aggregates.CheckDocumentCapacity, struct{}{})
		p11Expect(t, "document capacity inside Run", capacity, err, 4)
		shares, err := p11Run(t, alice, p11aggregates.CheckShareCapacity, struct{}{})
		p11Expect(t, "share capacity inside Run", shares, err, 6)
		pending, err := p11Run(t, alice, p11aggregates.CountPendingInvites, struct{}{})
		p11Expect(t, "pending invites inside Run", pending, err, 2)

		count, err := mia.Documents.Count(ctx)
		p11Expect(t, "caller document count outside Run", count, err, 1)
		count, err = mia.Documents.Count(ctx, p11aggregates.Documents.Where(p11aggregates.Documents.CollectionID.Eq(fixture.collect[0])))
		p11Expect(t, "caller collection count outside Run", count, err, 1)
		count, err = alice.Shares.Count(ctx)
		p11Expect(t, "caller share count outside Run", count, err, 1)
		count, err = alice.Invites.Count(ctx)
		p11Expect(t, "caller invite count outside Run", count, err, 1)
		count, err = alice.Assets.Count(ctx)
		p11Expect(t, "caller asset count outside Run", count, err, 1)
		err = alice.Transaction(ctx, func(tx *p11CallerTx) error {
			before, beforeErr := p11Sum(ctx, tx)
			p11Expect(t, "transaction asset sum before Run", before, beforeErr, 100)
			inside, runErr := p11aggregates.Run(ctx, tx, p11aggregates.CommitAssetBudget, p11aggregates.BudgetArgs{ArtifactID: fixture.artifacts[0]})
			p11Expect(t, "asset budget inside a caller transaction", inside, runErr, 357)
			after, afterErr := p11Sum(ctx, tx)
			p11Expect(t, "transaction asset sum after Run returns", after, afterErr, 100)
			counted, countErr := tx.Assets.Count(ctx)
			p11Expect(t, "transaction asset count after Run returns", counted, countErr, 1)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		err = mia.Transaction(ctx, func(tx *p11CallerTx) error {
			counted, countErr := tx.Documents.Count(ctx)
			p11Expect(t, "transaction document count outside Run", counted, countErr, 1)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestScopedAggregateGrantWidensRelationHopsConsistentlyAcrossProviders(t *testing.T) {
	forEachP11AggregateProfile(t, func(t *testing.T, fixture *p11Fixture) {
		mia := fixture.caller(t, p11aggregates.Principal{ID: "mia"})
		narrow, err := p11Run(t, mia, p11aggregates.CountManagedDocuments, struct{}{})
		p11Expect(t, "a Documents grant whose Collections hop the caller cannot read", narrow, err, 1)
		wide, err := p11Run(t, mia, p11aggregates.CheckDocumentCapacity, struct{}{})
		p11Expect(t, "a Documents grant whose Collections hop the same operation grants", wide, err, 4)
		carol := fixture.caller(t, p11aggregates.Principal{ID: "carol"})
		own, err := p11Run(t, carol, p11aggregates.CheckDocumentCapacity, struct{}{})
		p11Expect(t, "carol's document capacity", own, err, 1)
	})
}

func TestScopedAggregateFieldLimitsAreEnforcedAcrossProviders(t *testing.T) {
	forEachP11AggregateProfile(t, func(t *testing.T, fixture *p11Fixture) {
		alice := fixture.caller(t, p11aggregates.Principal{ID: "alice"})
		S := p11aggregates.Assets
		ran := map[string]bool{}
		p11aggregates.SetProbe(func(ctx context.Context, operation string, tx *p11CallerTx) error {
			if operation != "commitAssetBudget" {
				return nil
			}
			ran[operation] = true
			sum, err := p11Sum(ctx, tx)
			p11Expect(t, "Sum over a named field", sum, err, 357)
			minimum, maximum := S.Size.Min(), S.Size.Max()
			result, err := tx.Assets.Aggregate(ctx, S.Aggregate(S.AggregateSelect(minimum, maximum)))
			if err != nil {
				t.Errorf("Min and Max over a named field: %v", err)
			} else {
				low, _ := golem.AggregateValue(result, minimum).Get()
				high, _ := golem.AggregateValue(result, maximum).Get()
				if low != 7 || high != 250 {
					t.Errorf("Min/Max over a named field = %d/%d; want 7/250", low, high)
				}
			}
			counted, err := tx.Assets.Count(ctx, S.Where(S.ArtifactID.Eq(fixture.artifacts[0])))
			p11Expect(t, "Count filtered on a named field", counted, err, 3)
			counted, err = tx.Assets.Count(ctx)
			p11Expect(t, "unfiltered Count under a CanAggregate grant", counted, err, 3)
			_, err = tx.Assets.Aggregate(ctx, S.Aggregate(S.AggregateSelect(S.Label.Max())))
			p11Forbidden(t, "Max over an unnamed field", err)
			_, err = p11Sum(ctx, tx, S.Label.Eq("secret"))
			p11Forbidden(t, "Sum filtered on an unnamed field", err)
			_, err = tx.Assets.Count(ctx, S.Where(S.Label.Eq("secret")))
			p11Forbidden(t, "Count filtered on an unnamed field", err)
			_, err = tx.Assets.Count(ctx, S.Where(S.Owner.Eq("bob")))
			p11Forbidden(t, "Count filtered on an unnamed field the caller reads on its own rows", err)
			_, err = tx.Assets.Count(ctx, S.Where(S.Artifact.Is(p11aggregates.Artifacts.Title.Eq("alice's artifact"))))
			p11Forbidden(t, "Count filtered through an unnamed relation", err)
			return nil
		})
		if _, err := p11Run(t, alice, p11aggregates.CommitAssetBudget, p11aggregates.BudgetArgs{ArtifactID: fixture.artifacts[0]}); err != nil {
			t.Fatal(err)
		}
		mia := fixture.caller(t, p11aggregates.Principal{ID: "mia"})
		D := p11aggregates.Documents
		p11aggregates.SetProbe(func(ctx context.Context, operation string, tx *p11CallerTx) error {
			ran[operation] = true
			switch operation {
			case "checkDocumentCapacity":
				_, err := tx.Documents.Count(ctx, D.Where(D.CollectionID.Eq(fixture.collect[0])))
				p11Forbidden(t, "CanCount filtered on a field", err)
				_, err = tx.Documents.Count(ctx, D.Where(D.Title.Eq("document")))
				p11Forbidden(t, "CanCount filtered on another field", err)
				_, err = tx.Documents.Aggregate(ctx, D.Aggregate(D.AggregateSelect(D.Title.Max())))
				p11Forbidden(t, "CanCount measure over a field", err)
				all := D.CountAll()
				result, err := tx.Documents.Aggregate(ctx, D.Aggregate(D.AggregateSelect(all)))
				if err != nil {
					t.Errorf("CanCount aggregate CountAll: %v", err)
				} else if value, _ := golem.AggregateValue(result, all).Get(); value != 4 {
					t.Errorf("CanCount aggregate CountAll = %d; want 4", value)
				}
			}
			return nil
		})
		if _, err := p11Run(t, mia, p11aggregates.CheckDocumentCapacity, struct{}{}); err != nil {
			t.Fatal(err)
		}
		if len(ran) != 2 {
			t.Fatalf("the probe ran inside %v", ran)
		}
	})
}

func TestScopedAggregateGrantsNeverWidenRowReadsAcrossProviders(t *testing.T) {
	forEachP11AggregateProfile(t, func(t *testing.T, fixture *p11Fixture) {
		ctx := context.Background()
		alice := fixture.caller(t, p11aggregates.Principal{ID: "alice"})
		S, A := p11aggregates.Assets, p11aggregates.Artifacts
		publisher, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- fixture.app.RunEventPublisher(publisher) }()
		defer func() {
			stop()
			<-done
		}()
		deadline := time.Now().Add(5 * time.Second)
		for !fixture.app.EventCapabilities().PublisherRunning() {
			if time.Now().After(deadline) {
				t.Fatal("event publisher did not start")
			}
			time.Sleep(time.Millisecond)
		}
		explainOutside, err := alice.Assets.ExplainCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		aggregateOutside, err := alice.Assets.ExplainAggregate(ctx, S.Aggregate(S.AggregateSelect(S.Size.Sum())))
		if err != nil {
			t.Fatal(err)
		}
		var stream golem.EventStream[p11aggregates.AssetEvent]
		p11aggregates.SetProbe(func(ctx context.Context, operation string, tx *p11CallerTx) error {
			if operation != "commitAssetBudget" {
				return nil
			}
			rows, err := tx.Assets.FindMany(ctx)
			if err != nil || len(rows) != 1 {
				t.Errorf("FindMany inside Run = %d rows, %v; want the caller's 1", len(rows), err)
			}
			if _, found, err := tx.Assets.FindFirst(ctx, S.Where(S.ArtifactID.Eq(fixture.artifacts[0])), S.OrderBy(S.Size.Desc())); err != nil || !found {
				t.Errorf("FindFirst inside Run: found=%t err=%v", found, err)
			}
			if _, err := tx.Assets.FindUnique(ctx, S.ByID.Value(p10OperationID(t, 41))); err == nil {
				t.Error("FindUnique inside Run returned a row outside the read policy")
			}
			artifacts, err := tx.Artifacts.FindMany(ctx, A.Where(A.ID.Eq(fixture.artifacts[0])), A.Include(A.Assets.Select(S.ID)))
			if err != nil || len(artifacts) != 1 {
				t.Errorf("include inside Run = %d, %v", len(artifacts), err)
			} else if included, _ := golem.Many(artifacts[0], A.Assets.ToMany).Get(); len(included) != 1 {
				t.Errorf("include inside Run returned %d assets; want the caller's 1", len(included))
			}
			total := S.Size.Sum()
			groups, err := tx.Assets.GroupBy(ctx, S.GroupBy(S.GroupDimensions(S.ArtifactID.Dimension()), S.GroupMeasures(total)))
			if err != nil || len(groups) != 1 {
				t.Errorf("GroupBy inside Run = %d groups, %v; want the caller's 1", len(groups), err)
			} else if sum, _ := golem.GroupValue(groups[0], total).Get(); sum.String() != "100" {
				t.Errorf("GroupBy inside Run summed %s; want the caller's 100", sum)
			}
			explainInside, err := alice.Assets.ExplainCount(ctx)
			if err != nil || !reflect.DeepEqual(explainInside, explainOutside) {
				t.Errorf("ExplainCount inside Run differs from outside: %v", err)
			}
			aggregateInside, err := alice.Assets.ExplainAggregate(ctx, S.Aggregate(S.AggregateSelect(S.Size.Sum())))
			if err != nil || !reflect.DeepEqual(aggregateInside, aggregateOutside) {
				t.Errorf("ExplainAggregate inside Run differs from outside: %v", err)
			}
			stream, err = alice.Assets.Events(ctx)
			if err != nil {
				t.Errorf("Events inside Run: %v", err)
			}
			return nil
		})
		if _, err := p11Run(t, alice, p11aggregates.CommitAssetBudget, p11aggregates.BudgetArgs{ArtifactID: fixture.artifacts[0]}); err != nil {
			t.Fatal(err)
		}
		if stream == nil {
			t.Fatal("no event stream was opened inside Run")
		}
		defer func() { _ = stream.Close() }()
		system := fixture.app.System()
		for index, owner := range []string{"bob", "alice"} {
			if _, err := system.Assets.Create(ctx, S.Create(S.ID.Create(p10OperationID(t, 70+index)), S.ArtifactID.Create(fixture.artifacts[0]), S.Owner.Create(owner), S.Label.Create("late"), S.Size.Create(1))); err != nil {
				t.Fatal(err)
			}
		}
		receive, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		event, err := stream.Recv(receive)
		if err != nil {
			t.Fatal(err)
		}
		if event.ID() != p10OperationID(t, 71) {
			t.Fatalf("the first delivered event is %s; an event outside the read policy was delivered", event.ID())
		}
	})
}

func TestTxOperationOwnsACreateTheCallerCannotMakeAcrossProviders(t *testing.T) {
	forEachP11AggregateProfile(t, func(t *testing.T, fixture *p11Fixture) {
		ctx := context.Background()
		alice := fixture.caller(t, p11aggregates.Principal{ID: "alice"})
		C := p11aggregates.DocumentChanges
		direct := alice.Transaction(ctx, func(tx *p11CallerTx) error {
			_, err := tx.DocumentChanges.Create(ctx, C.Create(C.ID.Create(p10OperationID(t, 80)), C.DocumentID.Create(p10OperationID(t, 30)), C.Author.Create("alice"), C.Note.Create("direct")))
			return err
		})
		if direct == nil {
			t.Fatal("the caller created a document change outside the operation")
		}
		created, err := p11Run(t, alice, p11aggregates.RecordDocumentChange, p11aggregates.ChangeArgs{ID: p10OperationID(t, 81), DocumentID: p10OperationID(t, 30), Author: "alice", Note: "owned"})
		if err != nil || created != p10OperationID(t, 81).String() {
			t.Fatalf("operation-owned create = %q, %v", created, err)
		}
		forged := alice.Transaction(ctx, func(tx *p11CallerTx) error {
			_, err := p11aggregates.Run(ctx, tx, p11aggregates.RecordDocumentChange, p11aggregates.ChangeArgs{ID: p10OperationID(t, 82), DocumentID: p10OperationID(t, 30), Author: "bob", Note: "forged"})
			return err
		})
		if forged == nil {
			t.Fatal("the operation created a change outside its grant predicate")
		}
		count, err := alice.DocumentChanges.Count(ctx)
		p11Expect(t, "committed document changes", count, err, 1)
	})
}

func TestAggregateGrantsNamingAnythingButATxOperationFailThePolicyBuildAcrossProviders(t *testing.T) {
	forEachP11AggregateProfile(t, func(t *testing.T, fixture *p11Fixture) {
		ctx := context.Background()
		for _, principal := range []p11aggregates.Principal{{ID: "alice", Misconfigured: true}, {ID: "alice", Unregistered: true}} {
			caller := fixture.caller(t, principal)
			if _, err := caller.Assets.Count(ctx); err != nil {
				t.Fatalf("a misconfigured Artifact policy refused another model: %v", err)
			}
			_, err := caller.Artifacts.Count(ctx)
			var public *golem.Error
			if !errors.As(err, &public) || public.Code != golem.CodeUnauthenticated {
				t.Fatalf("%+v: an aggregate grant naming a non-transaction operation was accepted: %v", principal, err)
			}
			cause := errors.Unwrap(err)
			if cause == nil || !strings.Contains(cause.Error(), "transaction operation") {
				t.Fatalf("policy build error does not explain the refusal: %v (cause %v)", err, cause)
			}
		}
	})
}

func TestScopedAggregateGrantsNeverWidenWritesLinksOrRelationReadsAcrossProviders(t *testing.T) {
	forEachP11AggregateProfile(t, func(t *testing.T, fixture *p11Fixture) {
		ctx := context.Background()
		S, A := p11aggregates.Assets, p11aggregates.Artifacts
		ran := map[string]bool{}
		p11aggregates.SetProbe(func(ctx context.Context, operation string, tx *p11CallerTx) error {
			ran[operation] = true
			switch operation {
			case "commitAssetBudget":
				total := S.Size.Sum()
				groups, err := tx.Assets.RelationGroupBy(ctx, S.RelationGroupBy(S.RelationGroupDimensions(S.ArtifactTitle), S.RelationGroupMeasures(total)))
				if err != nil || len(groups) != 1 {
					t.Errorf("relationGroupBy inside Run = %d groups, %v; want the caller's 1", len(groups), err)
				} else if sum, _ := golem.RelationGroupValue(groups[0], total).Get(); sum.String() != "100" {
					t.Errorf("relationGroupBy inside Run summed %s; want the caller's 100", sum)
				}
				artifacts, err := tx.Artifacts.FindMany(ctx, A.Where(A.ID.Eq(fixture.artifacts[0])), A.Select(A.ID, A.Assets.Count()))
				if err != nil || len(artifacts) != 1 {
					t.Errorf("relation-count include inside Run = %d, %v", len(artifacts), err)
				} else if counted, _ := golem.RelationCount(artifacts[0], A.Assets.ToMany).Get(); counted != 1 {
					t.Errorf("relation-count include inside Run counted %d assets; want the caller's 1", counted)
				}
				updated, err := tx.Assets.UpdateMany(ctx, S.ArtifactID.Eq(fixture.artifacts[0]), S.UpdateMany(S.Label.Set("bumped")))
				p11Expect(t, "UpdateMany inside Run", updated, err, 1)
				if _, err := tx.Assets.Delete(ctx, S.ByID.Value(p10OperationID(t, 41))); err == nil {
					t.Error("Delete inside Run removed a row visible only through the aggregate grant")
				}
			case "checkDocumentCapacity":
				D, C := p11aggregates.Documents, p11aggregates.Collections
				if _, err := tx.Documents.Create(ctx, D.Create(D.ID.Create(p10OperationID(t, 90)), D.Owner.Create("mia"), D.Title.Create("own"), D.Collection.Connect(C.ByID.Value(fixture.collect[3])))); err != nil {
					t.Errorf("connect to the caller's own collection inside Run: %v", err)
				}
				if _, err := tx.Documents.Create(ctx, D.Create(D.ID.Create(p10OperationID(t, 91)), D.Owner.Create("mia"), D.Title.Create("linked"), D.Collection.Connect(C.ByID.Value(fixture.collect[0])))); err == nil {
					t.Error("connect inside Run reached a collection visible only through the aggregate grant")
				}
				if _, err := tx.Documents.Create(ctx, D.Create(D.ID.Create(p10OperationID(t, 92)), D.Owner.Create("mia"), D.Title.Create("keyed"), D.CollectionID.Create(fixture.collect[0]))); err == nil {
					t.Error("a foreign key inside Run reached a collection visible only through the aggregate grant")
				}
			}
			return nil
		})
		run := func(caller *p11aggregates.Caller[p11aggregates.Principal], body func(*p11CallerTx) error) {
			_ = caller.Transaction(ctx, body)
		}
		run(fixture.caller(t, p11aggregates.Principal{ID: "alice"}), func(tx *p11CallerTx) error {
			_, err := p11aggregates.Run(ctx, tx, p11aggregates.CommitAssetBudget, p11aggregates.BudgetArgs{ArtifactID: fixture.artifacts[0]})
			return err
		})
		run(fixture.caller(t, p11aggregates.Principal{ID: "mia"}), func(tx *p11CallerTx) error {
			_, err := p11aggregates.Run(ctx, tx, p11aggregates.CheckDocumentCapacity, struct{}{})
			return err
		})
		if !ran["commitAssetBudget"] || !ran["checkDocumentCapacity"] {
			t.Fatalf("the probe ran inside %v", ran)
		}
		row, err := fixture.app.System().Assets.FindUnique(ctx, S.ByID.Value(p10OperationID(t, 41)), S.Select(S.Label))
		if err != nil {
			t.Fatalf("the asset outside the caller's policy was removed: %v", err)
		}
		if label, _ := golem.Value(row, S.Label).Get(); label != "draft" {
			t.Fatalf("a write inside Run changed a row outside the caller's policy: label %q", label)
		}
	})
}
