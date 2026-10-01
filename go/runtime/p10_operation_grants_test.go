package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

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
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
	"github.com/jmoiron/sqlx"
)

const (
	p10OperationNamespace       physical.PhysicalName = "golem_p10_operations"
	p10OperationSystemNamespace physical.PhysicalName = "golem_p10_operations_system"
)

type p10OperationPrincipalKey struct{}

type p10OperationLowerer struct{ delegate physical.Lowerer }

func (lowerer p10OperationLowerer) Manifest() physical.ProviderManifest {
	return lowerer.delegate.Manifest()
}

func (lowerer p10OperationLowerer) Lower(ctx context.Context, model compilerir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	options.Namespace = p10OperationNamespace
	schema, err := lowerer.delegate.Lower(ctx, model, options)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	schema.System.Namespace.Name = p10OperationSystemNamespace
	return physical.Normalize(schema)
}

type p10OperationRecord struct {
	kind       observe.Kind
	operation  observe.Operation
	phase      observe.Phase
	outcome    observe.Outcome
	reason     observe.Reason
	model      golem.ModelID
	statements int
}

type p10OperationObserver struct {
	mu      sync.Mutex
	records []p10OperationRecord
}

func (observer *p10OperationObserver) ObserveGolem(_ context.Context, value observe.Observation) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.records = append(observer.records, p10OperationRecord{
		kind: value.Kind(), operation: value.Operation(), phase: value.Phase(), outcome: value.Outcome(),
		reason: value.Reason(), model: value.ModelID(), statements: value.StatementCount(),
	})
}

func (observer *p10OperationObserver) reset() {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.records = nil
}

func (observer *p10OperationObserver) snapshot(exclude observe.Kind) []p10OperationRecord {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	var result []p10OperationRecord
	for _, record := range observer.records {
		if record.kind != exclude {
			result = append(result, record)
		}
	}
	return result
}

type p10OperationFixture struct {
	app      *p10operations.App[p10operations.Principal]
	server   *p10operations.GraphQLServer
	database *sqlx.DB
	gate     *p10CommitGate
	observer *p10OperationObserver
	alpha    golem.UUID
	beta     golem.UUID
}

type p10OperationGraphQLResponse = p5ExtensionGraphQLResponse

func p10OperationID(t *testing.T, value int) golem.UUID {
	t.Helper()
	id, err := golem.ParseUUID(fmt.Sprintf("00000000-0000-0000-0000-%012d", value))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func forEachP10OperationProfile(t *testing.T, run func(*testing.T, *p10OperationFixture)) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			p10operations.Reset(nil)
			t.Cleanup(func() { p10operations.Reset(nil) })
			run(t, newP10OperationFixture(t, profile))
		})
	}
}

func newP10OperationFixture(t *testing.T, profile p5ExtensionProviderProfile) *p10OperationFixture {
	t.Helper()
	ctx := context.Background()
	gate := &p10CommitGate{}
	database := openP10OperationDatabase(t, profile, gate)
	apply := sqliteprovider.New().ApplyInitial
	if profile.provider == golem.PostgreSQL {
		apply = postgresprovider.New().ApplyInitial
	}
	var encoded []byte
	for _, document := range p10operations.GolemGeneratedSchemaBundle().Providers() {
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
	observer := &p10OperationObserver{}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	application, err := p10operations.Open(ctx, p10operations.Config[p10operations.Principal]{
		Database:            p8AdoptTracedProviderHandle(database, profile),
		Observer:            observer,
		EventTransport:      transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			t.Errorf("after-commit hook failed: %v", failure.Cause())
		},
		ResolvePrincipal: func(_ context.Context, principal p10operations.Principal) (p10operations.Actor, error) {
			if !principal.Valid {
				return p10operations.Actor{}, golem.RuntimeReadError(golem.CodeUnauthenticated, "graphql", golem.ModelID{}, golem.FieldID{}, "invalid principal", nil)
			}
			return p10operations.Actor{ID: principal.ID, Misconfigured: principal.Misconfigured}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := application.GraphQL(p10operations.GraphQLConfig[p10operations.Principal]{
		PrincipalFromContext: func(ctx context.Context) (p10operations.Principal, bool) {
			principal, ok := ctx.Value(p10OperationPrincipalKey{}).(p10operations.Principal)
			return principal, ok
		},
		ReportInternalError: func(_ context.Context, err error) { t.Logf("reported GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &p10OperationFixture{app: application, server: server, database: database, gate: gate, observer: observer, alpha: p10OperationID(t, 1), beta: p10OperationID(t, 2)}
	system := application.System()
	for owner, team := range map[string]golem.UUID{"alpha": fixture.alpha, "beta": fixture.beta} {
		if _, err := system.Teams.Create(ctx, p10operations.Teams.Create(p10operations.Teams.ID.Create(team), p10operations.Teams.Owner.Create(owner))); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (fixture *p10OperationFixture) caller(t *testing.T, id string) *p10operations.Caller[p10operations.Principal] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), p10operations.Principal{ID: id, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func (fixture *p10OperationFixture) graphql(t *testing.T, id, query string) p10OperationGraphQLResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), p10OperationPrincipalKey{}, p10operations.Principal{ID: id, Valid: true})
	response, err := executeP10OperationGraphQL(ctx, fixture.server, query)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func executeP10OperationGraphQL(ctx context.Context, server *p10operations.GraphQLServer, query string) (p10OperationGraphQLResponse, error) {
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return p10OperationGraphQLResponse{}, err
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	var response p10OperationGraphQLResponse
	decoder := json.NewDecoder(recorder.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return p10OperationGraphQLResponse{}, fmt.Errorf("decode GraphQL status=%d: %w", recorder.Code, err)
	}
	return response, nil
}

func (fixture *p10OperationFixture) inviteExists(t *testing.T, id golem.UUID) (bool, string, string) {
	t.Helper()
	row, err := fixture.app.System().Invites.FindUnique(context.Background(), p10operations.Invites.ByID.Value(id), p10operations.Invites.Select(p10operations.Invites.Email, p10operations.Invites.Status))
	if err != nil {
		var failure *golem.Error
		if errors.As(err, &failure) && failure.Code == golem.CodeNotFound {
			return false, "", ""
		}
		t.Fatal(err)
	}
	email, _ := golem.Value(row, p10operations.Invites.Email).Get()
	status, _ := golem.Value(row, p10operations.Invites.Status).Get()
	return true, email, status
}

func (fixture *p10OperationFixture) inviteArgs(id golem.UUID, email string) p10operations.InviteArgs {
	return p10operations.InviteArgs{ID: id, TeamID: fixture.alpha, Owner: "alpha", Email: email}
}

func (fixture *p10OperationFixture) genericCreate(ctx context.Context, caller *p10operations.Caller[p10operations.Principal], id golem.UUID) error {
	_, err := caller.Invites.Create(ctx, p10operations.Invites.Create(
		p10operations.Invites.ID.Create(id), p10operations.Invites.TeamID.Create(fixture.alpha),
		p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("generic@example.test"),
		p10operations.Invites.Status.Create("pending"),
	))
	return err
}

func (fixture *p10OperationFixture) seedInvite(t *testing.T, id golem.UUID) {
	t.Helper()
	if _, err := fixture.app.System().Invites.Create(context.Background(), p10operations.Invites.Create(
		p10operations.Invites.ID.Create(id), p10operations.Invites.TeamID.Create(fixture.alpha),
		p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("seeded@example.test"),
		p10operations.Invites.Status.Create("pending"),
	)); err != nil {
		t.Fatal(err)
	}
}

func assertP10SameRefusal(t *testing.T, label string, want, got error) {
	t.Helper()
	var wanted, actual *golem.Error
	if !errors.As(want, &wanted) || !errors.As(got, &actual) {
		t.Fatalf("%s: refusals are not public errors: want=%v got=%v", label, want, got)
	}
	if wanted.Code != actual.Code || wanted.Operation != actual.Operation || wanted.Model != actual.Model || wanted.Field != actual.Field || wanted.Message != actual.Message || want.Error() != got.Error() {
		t.Fatalf("%s: refusal differs from the caller's own refusal\nwant=%#v (%v)\ngot=%#v (%v)", label, wanted, want, actual, got)
	}
}

func p10GraphQLCode(t *testing.T, response p10OperationGraphQLResponse) string {
	t.Helper()
	if len(response.Errors) == 0 {
		t.Fatalf("GraphQL response has no error: %#v", response.Data)
	}
	code, _ := response.Errors[0].Extensions["code"].(string)
	return code
}

func TestOperationGrantLetsOnlyItsOwningOperationWriteAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		generic := fixture.genericCreate(ctx, caller, p10OperationID(t, 100))
		if generic == nil {
			t.Fatal("the caller created a model its policy closes")
		}
		_, direct := p10operations.InviteMember(ctx, caller, fixture.inviteArgs(p10OperationID(t, 101), "direct@example.test"))
		assertP10SameRefusal(t, "direct resolver call", generic, direct)
		var failure *golem.Error
		errors.As(generic, &failure)
		response := fixture.graphql(t, "alpha", fmt.Sprintf(`mutation { createInvite(data: {id: "%s", owner: "alpha", email: "g@example.test", status: "pending", team: {connect: {ID: "%s"}}}) { id } }`, p10OperationID(t, 102), fixture.alpha))
		if code := p10GraphQLCode(t, response); code != string(failure.Code) {
			t.Fatalf("generic GraphQL create code=%s want %s", code, failure.Code)
		}
		if _, err := p10operations.Mutate(ctx, caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 103), "not-an-email")); !errors.Is(err, p10operations.ErrInvalidEmail) {
			t.Fatalf("the operation's own validation did not run: %v", err)
		}
		created, err := p10operations.Mutate(ctx, caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 104), "  Member@Example.TEST "))
		if err != nil || created != p10OperationID(t, 104).String() {
			t.Fatalf("Mutate through the owning operation = %q, %v", created, err)
		}
		graphql := fixture.graphql(t, "alpha", fmt.Sprintf(`mutation { inviteMember(id: "%s", teamID: "%s", owner: "alpha", email: " Other@Example.TEST", fail: false) }`, p10OperationID(t, 105), fixture.alpha))
		if len(graphql.Errors) != 0 || graphql.Data["inviteMember"] != p10OperationID(t, 105).String() {
			t.Fatalf("GraphQL owning operation data=%#v errors=%#v", graphql.Data, graphql.Errors)
		}
		for id, want := range map[int]string{104: "member@example.test", 105: "other@example.test"} {
			if exists, email, _ := fixture.inviteExists(t, p10OperationID(t, id)); !exists || email != want {
				t.Fatalf("invite %d exists=%t email=%q want %q", id, exists, email, want)
			}
		}
		for _, id := range []int{100, 101, 102, 103} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists {
				t.Fatalf("refused invite %d was written", id)
			}
		}
		if _, err := p10operations.Mutate(ctx, fixture.caller(t, "beta"), p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 106), "beta@example.test")); err == nil {
			t.Fatal("the operation grant wrote a row outside its predicate")
		}
	})
}

func TestOperationGrantEndsWhenTheResolverReturnsAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		generic := fixture.genericCreate(ctx, caller, p10OperationID(t, 200))
		var retained *p10operations.Caller[p10operations.Principal]
		var waitedErr error
		outlive := make(chan struct{})
		outlived := make(chan error, 1)
		p10operations.Reset(func(ctx context.Context, _ string, operation *p10operations.Caller[p10operations.Principal]) error {
			retained = operation
			var wait sync.WaitGroup
			wait.Add(1)
			go func() {
				defer wait.Done()
				waitedErr = fixture.genericCreate(ctx, operation, p10OperationID(t, 201))
			}()
			wait.Wait()
			go func() {
				<-outlive
				outlived <- fixture.genericCreate(context.Background(), operation, p10OperationID(t, 202))
			}()
			return nil
		})
		if _, err := p10operations.Mutate(ctx, caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 203), "owner@example.test")); err != nil {
			t.Fatal(err)
		}
		if waitedErr != nil {
			t.Fatalf("a goroutine the operation waited for lost the grant: %v", waitedErr)
		}
		p10operations.Reset(nil)
		assertP10SameRefusal(t, "retained caller", generic, fixture.genericCreate(ctx, retained, p10OperationID(t, 204)))
		close(outlive)
		assertP10SameRefusal(t, "goroutine that outlived the operation", generic, <-outlived)
		if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, 201)); !exists {
			t.Fatal("the waited goroutine's write is missing")
		}
		for _, id := range []int{202, 204} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists {
				t.Fatalf("a write after the operation returned was committed: %d", id)
			}
		}
		if _, err := retained.Invites.FindUnique(ctx, p10operations.Invites.ByID.Value(p10OperationID(t, 203))); err != nil {
			t.Fatalf("a retained caller lost the caller's own read policy: %v", err)
		}
	})
}

func TestTwoMutationRootsEachReceiveOnlyTheirOwnGrantAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		fixture.seedInvite(t, p10OperationID(t, 300))
		generic := fixture.genericCreate(context.Background(), fixture.caller(t, "alpha"), p10OperationID(t, 301))
		var first *p10operations.Caller[p10operations.Principal]
		var ownErr, borrowedErr error
		p10operations.Reset(func(ctx context.Context, operation string, caller *p10operations.Caller[p10operations.Principal]) error {
			switch operation {
			case "inviteMember":
				first = caller
			case "acceptInvite":
				ownErr = fixture.genericCreate(ctx, caller, p10OperationID(t, 302))
				borrowedErr = fixture.genericCreate(ctx, first, p10OperationID(t, 303))
			}
			return nil
		})
		response := fixture.graphql(t, "alpha", fmt.Sprintf(`mutation {
  first: inviteMember(id: "%s", teamID: "%s", owner: "alpha", email: "first@example.test", fail: false)
  second: acceptInvite(id: "%s", alsoEmail: false)
}`, p10OperationID(t, 304), fixture.alpha, p10OperationID(t, 300)))
		if len(response.Errors) != 0 || response.Data["first"] != p10OperationID(t, 304).String() || response.Data["second"] != "accepted" {
			t.Fatalf("two-root document data=%#v errors=%#v", response.Data, response.Errors)
		}
		assertP10SameRefusal(t, "second root using its own caller", generic, ownErr)
		assertP10SameRefusal(t, "second root using the first root's caller", generic, borrowedErr)
		for _, id := range []int{302, 303} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists {
				t.Fatalf("the second root wrote with a grant it does not own: %d", id)
			}
		}
	})
}

func TestWithinFieldGrantLeavesCallerDenialsOnUncoveredFieldsAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		id := p10OperationID(t, 400)
		fixture.seedInvite(t, id)
		_, statusErr := caller.Invites.Update(ctx, p10operations.Invites.ByID.Value(id), p10operations.Invites.Update(p10operations.Invites.Status.Set("accepted")))
		if statusErr == nil {
			t.Fatal("the caller updated a field its policy denies")
		}
		_, emailErr := caller.Invites.Update(ctx, p10operations.Invites.ByID.Value(id), p10operations.Invites.Update(p10operations.Invites.Status.Set("accepted"), p10operations.Invites.Email.Set("changed@example.test")))
		_, operationErr := p10operations.Mutate(ctx, caller, p10operations.AcceptInvite, p10operations.AcceptArgs{ID: id, AlsoEmail: true})
		assertP10SameRefusal(t, "field outside the Within grant", emailErr, operationErr)
		if _, _, status := fixture.inviteExists(t, id); status != "pending" {
			t.Fatalf("a refused field update changed status to %q", status)
		}
		accepted, err := p10operations.Mutate(ctx, caller, p10operations.AcceptInvite, p10operations.AcceptArgs{ID: id})
		if err != nil || accepted != "accepted" {
			t.Fatalf("Within field grant = %q, %v", accepted, err)
		}
		if _, email, status := fixture.inviteExists(t, id); status != "accepted" || email != "seeded@example.test" {
			t.Fatalf("after the operation status=%q email=%q", status, email)
		}
	})
}

func TestOperationGrantReachesNestedUpsertAndTransactionalWritesAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		_, genericNested := caller.Teams.Create(ctx, p10operations.Teams.Create(
			p10operations.Teams.ID.Create(p10OperationID(t, 500)), p10operations.Teams.Owner.Create("alpha"),
			p10operations.Teams.Invites.Create(p10operations.Invites.Create(
				p10operations.Invites.ID.Create(p10OperationID(t, 501)), p10operations.Invites.Owner.Create("alpha"),
				p10operations.Invites.Email.Create("nested@example.test"), p10operations.Invites.Status.Create("pending"),
			)),
		))
		if genericNested == nil {
			t.Fatal("the caller created a closed model through a nested write")
		}
		nested := p10operations.InviteArgs{ID: p10OperationID(t, 502), TeamID: p10OperationID(t, 503), Owner: "alpha", Email: "Nested@Example.TEST"}
		if _, err := p10operations.Mutate(ctx, caller, p10operations.NestedTeamInvite, nested); err != nil {
			t.Fatalf("nested write inside the operation: %v", err)
		}
		if exists, email, _ := fixture.inviteExists(t, nested.ID); !exists || email != "nested@example.test" {
			t.Fatalf("nested invite exists=%t email=%q", exists, email)
		}

		_, genericUpsert := caller.Invites.Upsert(ctx, p10operations.Invites.ByID.Value(p10OperationID(t, 504)),
			p10operations.Invites.Create(p10operations.Invites.ID.Create(p10OperationID(t, 504)), p10operations.Invites.TeamID.Create(fixture.alpha), p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("u@example.test"), p10operations.Invites.Status.Create("pending")),
			p10operations.Invites.Update(p10operations.Invites.Status.Set("renewed")),
		)
		if genericUpsert == nil {
			t.Fatal("the caller upserted a closed model")
		}
		upsert := fixture.inviteArgs(p10OperationID(t, 505), "upsert@example.test")
		for _, want := range []string{"pending", "renewed"} {
			status, err := p10operations.Mutate(ctx, caller, p10operations.UpsertInvite, upsert)
			if err != nil || status != want {
				t.Fatalf("upsert inside the operation = %q, %v; want %q", status, err, want)
			}
		}

		committed := fixture.inviteArgs(p10OperationID(t, 506), "tx@example.test")
		if _, err := p10operations.Mutate(ctx, caller, p10operations.TransactionalInvite, committed); err != nil {
			t.Fatalf("transactional write inside the operation: %v", err)
		}
		rolledBack := fixture.inviteArgs(p10OperationID(t, 507), "rollback@example.test")
		rolledBack.Fail = true
		if _, err := p10operations.Mutate(ctx, caller, p10operations.TransactionalInvite, rolledBack); err == nil {
			t.Fatal("requested rollback succeeded")
		}
		if exists, _, _ := fixture.inviteExists(t, committed.ID); !exists {
			t.Fatal("the committed transactional invite is missing")
		}
		if exists, _, _ := fixture.inviteExists(t, rolledBack.ID); exists {
			t.Fatal("the rolled-back transactional invite persisted")
		}
	})
}

func TestOperationGrantKeepsTheReadToLinkRuleAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		absent := p10operations.InviteArgs{ID: p10OperationID(t, 600), TeamID: p10OperationID(t, 699), Owner: "alpha", Email: "absent@example.test"}
		_, absentErr := p10operations.Mutate(ctx, caller, p10operations.InviteMember, absent)
		if absentErr == nil {
			t.Fatal("the operation linked to a team that does not exist")
		}
		unreadable := p10operations.InviteArgs{ID: p10OperationID(t, 601), TeamID: fixture.beta, Owner: "alpha", Email: "link@example.test"}
		_, unreadableErr := p10operations.Mutate(ctx, caller, p10operations.InviteMember, unreadable)
		assertP10SameRefusal(t, "link to an unreadable team", absentErr, unreadableErr)
		for _, id := range []golem.UUID{absent.ID, unreadable.ID} {
			if exists, _, _ := fixture.inviteExists(t, id); exists {
				t.Fatal("the operation linked to a team the caller cannot read")
			}
		}
	})
}

func TestMutateAndGraphQLDispatchRunTheSameOperationAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		for index, operation := range []struct {
			name    string
			mutate  func(p10operations.InviteArgs) error
			graphql string
		}{
			{"inviteMember", func(arguments p10operations.InviteArgs) error {
				_, err := p10operations.Mutate(ctx, caller, p10operations.InviteMember, arguments)
				return err
			}, "inviteMember"},
			{"transactionalInvite", func(arguments p10operations.InviteArgs) error {
				_, err := p10operations.Mutate(ctx, caller, p10operations.TransactionalInvite, arguments)
				return err
			}, "transactionalInvite"},
		} {
			programmatic := fixture.inviteArgs(p10OperationID(t, 700+index*10), "parity@example.test")
			fixture.observer.reset()
			p10operations.Reset(nil)
			if err := operation.mutate(programmatic); err != nil {
				t.Fatalf("%s through Mutate: %v", operation.name, err)
			}
			goRecords, goHooks := fixture.observer.snapshot(observe.KindGraphQL), p10operations.Snapshot()

			fixture.observer.reset()
			p10operations.Reset(nil)
			response := fixture.graphql(t, "alpha", fmt.Sprintf(`mutation { %s(id: "%s", teamID: "%s", owner: "alpha", email: "parity@example.test", fail: false) }`, operation.graphql, p10OperationID(t, 701+index*10), fixture.alpha))
			if len(response.Errors) != 0 {
				t.Fatalf("%s through GraphQL: %#v", operation.name, response.Errors)
			}
			graphqlRecords, graphqlHooks := fixture.observer.snapshot(observe.KindGraphQL), p10operations.Snapshot()

			if !reflect.DeepEqual(goRecords, graphqlRecords) {
				t.Fatalf("%s observations differ\nMutate:  %+v\nGraphQL: %+v", operation.name, goRecords, graphqlRecords)
			}
			if !reflect.DeepEqual(goHooks, graphqlHooks) || fmt.Sprint(goHooks.Hooks) != "[before_create after_create after_commit_create]" {
				t.Fatalf("%s hooks differ Mutate=%+v GraphQL=%+v", operation.name, goHooks, graphqlHooks)
			}
			custom := 0
			for _, record := range goRecords {
				if record.kind == observe.KindMutation && record.operation == observe.OperationMutationCustom {
					custom++
					if record.outcome != observe.OutcomeSuccess {
						t.Fatalf("%s custom mutation outcome=%s", operation.name, record.outcome)
					}
				}
			}
			if custom != 1 {
				t.Fatalf("%s custom mutation records=%d want 1: %+v", operation.name, custom, goRecords)
			}
		}
	})
}

func TestMutateRefusesAFunctionThatIsNotACustomMutationBeforeRunningAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		wrapped := func(ctx context.Context, caller *p10operations.Caller[p10operations.Principal], arguments p10operations.InviteArgs) (string, error) {
			return p10operations.InviteMember(ctx, caller, arguments)
		}
		fixture.observer.reset()
		p10operations.Reset(nil)
		refusals := map[string]error{}
		_, refusals["query resolver"] = p10operations.Mutate(ctx, caller, p10operations.SearchInvites, p10operations.SearchArgs{Owner: "alpha"})
		_, refusals["wrapped resolver"] = p10operations.Mutate(ctx, caller, wrapped, fixture.inviteArgs(p10OperationID(t, 800), "wrapped@example.test"))
		_, refusals["nil caller"] = p10operations.Mutate(ctx, nil, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 801), "nil@example.test"))
		for name, err := range refusals {
			if err == nil || !strings.Contains(err.Error(), "P5_CUSTOM_OPERATION") {
				t.Fatalf("%s: Mutate = %v", name, err)
			}
		}
		if records := fixture.observer.snapshot(""); len(records) != 0 {
			t.Fatalf("refused Mutate calls were observed: %+v", records)
		}
		if record := p10operations.Snapshot(); len(record.Resolvers) != 0 || len(record.Hooks) != 0 {
			t.Fatalf("refused Mutate calls ran application code: %+v", record)
		}
		for _, id := range []int{800, 801} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists {
				t.Fatalf("refused Mutate call wrote invite %d", id)
			}
		}
	})
}

func TestWithinNamingANonMutationResolverFailsThePolicyBuildAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		_, err := fixture.app.ForPrincipal(context.Background(), p10operations.Principal{ID: "alpha", Valid: true, Misconfigured: true})
		if err == nil {
			t.Fatal("a policy scoping a grant to a query resolver was accepted")
		}
		cause := errors.Unwrap(err)
		if cause == nil || !strings.Contains(cause.Error(), "SearchInvites") || !strings.Contains(cause.Error(), "custom mutation") {
			t.Fatalf("policy build error does not name the resolver: %v (cause %v)", err, cause)
		}
	})
}

func TestP10OperationFixtureRegeneratesByteIdentically(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	directory := filepath.Join(moduleRoot, "runtime", "testdata", "p10operations")
	request := pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/runtime/testdata/p10operations", PackageName: "p10operations", Directory: directory},
		Lowerers:   []physical.Lowerer{p10OperationLowerer{delegate: postgresprovider.New()}, sqliteprovider.New()},
	}
	result, err := pipeline.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range p5ExtensionArtifactMap(result) {
		if !strings.HasPrefix(path, "runtime/testdata/p10operations/") {
			continue
		}
		committed, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, committed) {
			t.Fatalf("committed operations fixture is stale: %s: %s", path, p5ExtensionFirstDifference(committed, content))
		}
	}
}
