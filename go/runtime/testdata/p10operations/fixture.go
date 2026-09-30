package p10operations

import (
	"context"
	"errors"
	"strings"
	"sync"

	golem "github.com/eleven-am/golem/go/golem"
)

type Principal struct {
	ID            string
	Valid         bool
	Misconfigured bool
}

type Actor struct {
	ID            string
	Misconfigured bool
}

type Team struct {
	_ struct{} `golem:"model;id=p10operations.Team;table=teams;graphql=Team"`

	ID      golem.UUID `db:"id" golem:"id=p10operations.Team.ID;pk"`
	Owner   string     `db:"owner" golem:"type=varchar(80)"`
	Invites []Invite   `db:"-" golem:"relation=has_many;fields=id;references=team_id"`
}

type Invite struct {
	_ struct{} `golem:"model;id=p10operations.Invite;table=invites;graphql=Invite"`

	ID     golem.UUID `db:"id" golem:"id=p10operations.Invite.ID;pk"`
	TeamID golem.UUID `db:"team_id"`
	Owner  string     `db:"owner" golem:"type=varchar(80)"`
	Email  string     `db:"email" golem:"type=varchar(160)"`
	Status string     `db:"status" golem:"type=varchar(40)"`
	Team   *Team      `db:"-" golem:"relation=belongs_to;fields=team_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "p10_operations")
	golem.Actor[Actor](schema)
	golem.Model[Team](schema)
	golem.Model[Invite](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (Team) DefinePolicy(rules *golem.Rules[Team], actor Actor) {
	owned := Teams.Owner.Eq(actor.ID)
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
}

func (Invite) DefinePolicy(rules *golem.Rules[Invite], actor Actor) {
	owned := Invites.Owner.Eq(actor.ID)
	rules.CanRead(owned)
	rules.CanUpdate(owned)
	rules.CannotUpdateFields(golem.All[Invite](), Invites.Status, Invites.Email)
	golem.Within(rules, InviteMember).CanCreate(owned)
	golem.Within(rules, NestedTeamInvite).CanCreate(owned)
	golem.Within(rules, TransactionalInvite).CanCreate(owned)
	golem.Within(rules, UpsertInvite).CanCreate(owned)
	golem.Within(rules, UpsertInvite).CanUpdateFields(owned, Invites.Status)
	golem.Within(rules, AcceptInvite).CanUpdateFields(owned, Invites.Status)
	if actor.Misconfigured {
		golem.Within(rules, SearchInvites).CanCreate(owned)
	}
}

func (Invite) BeforeCreate(ctx context.Context, _ *InviteCreateRequest) error {
	recordHook("before_create")
	return nil
}

func (Invite) AfterCreate(_ context.Context, _ InviteCreateResult) error {
	recordHook("after_create")
	return nil
}

func (Invite) AfterCommitCreate(_ context.Context, _ InviteCreateResult) error {
	recordHook("after_commit_create")
	return nil
}

type InviteArgs struct {
	ID     golem.UUID `golem:"graphql=id"`
	TeamID golem.UUID `golem:"graphql=teamID"`
	Owner  string     `golem:"graphql=owner"`
	Email  string     `golem:"graphql=email"`
	Fail   bool       `golem:"graphql=fail"`
}

type AcceptArgs struct {
	ID        golem.UUID `golem:"graphql=id"`
	AlsoEmail bool       `golem:"graphql=alsoEmail"`
}

type SearchArgs struct {
	Owner string `golem:"graphql=owner"`
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "inviteMember", InviteMember)
	golem.Mutation(graphql, "acceptInvite", AcceptInvite)
	golem.Mutation(graphql, "nestedTeamInvite", NestedTeamInvite)
	golem.Mutation(graphql, "transactionalInvite", TransactionalInvite)
	golem.Mutation(graphql, "renewInvite", UpsertInvite)
	golem.Query(graphql, "searchInvites", SearchInvites)
}

var ErrInvalidEmail = errors.New("invite email is invalid")

func normalizedEmail(value string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(value))
	if !strings.Contains(email, "@") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

func InviteMember(ctx context.Context, caller *Caller[Principal], arguments InviteArgs) (string, error) {
	recordResolver("inviteMember")
	if err := runProbe(ctx, "inviteMember", caller); err != nil {
		return "", err
	}
	email, err := normalizedEmail(arguments.Email)
	if err != nil {
		return "", err
	}
	created, err := caller.Invites.Create(ctx, inviteInput(arguments, email), Invites.Select(Invites.ID))
	if err != nil {
		return "", err
	}
	id, _ := golem.Value(created, Invites.ID).Get()
	return id.String(), nil
}

func AcceptInvite(ctx context.Context, caller *Caller[Principal], arguments AcceptArgs) (string, error) {
	recordResolver("acceptInvite")
	if err := runProbe(ctx, "acceptInvite", caller); err != nil {
		return "", err
	}
	input := Invites.Update(Invites.Status.Set("accepted"))
	if arguments.AlsoEmail {
		input = Invites.Update(Invites.Status.Set("accepted"), Invites.Email.Set("changed@example.test"))
	}
	updated, err := caller.Invites.Update(ctx, Invites.ByID.Value(arguments.ID), input, Invites.Select(Invites.Status))
	if err != nil {
		return "", err
	}
	status, _ := golem.Value(updated, Invites.Status).Get()
	return status, nil
}

func NestedTeamInvite(ctx context.Context, caller *Caller[Principal], arguments InviteArgs) (string, error) {
	recordResolver("nestedTeamInvite")
	email, err := normalizedEmail(arguments.Email)
	if err != nil {
		return "", err
	}
	created, err := caller.Teams.Create(ctx, Teams.Create(
		Teams.ID.Create(arguments.TeamID),
		Teams.Owner.Create(arguments.Owner),
		Teams.Invites.Create(Invites.Create(
			Invites.ID.Create(arguments.ID),
			Invites.Owner.Create(arguments.Owner),
			Invites.Email.Create(email),
			Invites.Status.Create("pending"),
		)),
	), Teams.Select(Teams.ID))
	if err != nil {
		return "", err
	}
	id, _ := golem.Value(created, Teams.ID).Get()
	return id.String(), nil
}

func TransactionalInvite(ctx context.Context, caller *Caller[Principal], arguments InviteArgs) (string, error) {
	recordResolver("transactionalInvite")
	email, err := normalizedEmail(arguments.Email)
	if err != nil {
		return "", err
	}
	var result string
	err = caller.Transaction(ctx, func(transaction *CallerTx[Principal]) error {
		created, createErr := transaction.Invites.Create(ctx, inviteInput(arguments, email), Invites.Select(Invites.ID))
		if createErr != nil {
			return createErr
		}
		id, _ := golem.Value(created, Invites.ID).Get()
		result = id.String()
		if arguments.Fail {
			return errors.New("requested operation rollback")
		}
		return nil
	})
	return result, err
}

func UpsertInvite(ctx context.Context, caller *Caller[Principal], arguments InviteArgs) (string, error) {
	recordResolver("upsertInvite")
	email, err := normalizedEmail(arguments.Email)
	if err != nil {
		return "", err
	}
	upserted, err := caller.Invites.Upsert(ctx, Invites.ByID.Value(arguments.ID), inviteInput(arguments, email), Invites.Update(Invites.Status.Set("renewed")), Invites.Select(Invites.Status))
	if err != nil {
		return "", err
	}
	status, _ := golem.Value(upserted, Invites.Status).Get()
	return status, nil
}

func SearchInvites(ctx context.Context, caller *Caller[Principal], arguments SearchArgs) ([]golem.Row[Invite], error) {
	recordResolver("searchInvites")
	return caller.Invites.FindMany(ctx, Invites.Where(Invites.Owner.Eq(arguments.Owner)), Invites.OrderBy(Invites.ID.Asc()), Invites.Take(20), Invites.Select(Invites.ID, Invites.Status))
}

func inviteInput(arguments InviteArgs, email string) InviteCreateInput {
	return Invites.Create(
		Invites.ID.Create(arguments.ID),
		Invites.TeamID.Create(arguments.TeamID),
		Invites.Owner.Create(arguments.Owner),
		Invites.Email.Create(email),
		Invites.Status.Create("pending"),
	)
}

type Probe func(ctx context.Context, operation string, caller *Caller[Principal]) error

type Record struct {
	Resolvers []string
	Hooks     []string
}

var (
	probeLock   sync.Mutex
	activeProbe Probe
	record      Record
)

func Reset(probe Probe) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeProbe = probe
	record = Record{}
}

func Snapshot() Record {
	probeLock.Lock()
	defer probeLock.Unlock()
	return Record{Resolvers: append([]string(nil), record.Resolvers...), Hooks: append([]string(nil), record.Hooks...)}
}

func runProbe(ctx context.Context, operation string, caller *Caller[Principal]) error {
	probeLock.Lock()
	probe := activeProbe
	probeLock.Unlock()
	if probe == nil {
		return nil
	}
	return probe(ctx, operation, caller)
}

func recordResolver(name string) {
	probeLock.Lock()
	defer probeLock.Unlock()
	record.Resolvers = append(record.Resolvers, name)
}

func recordHook(name string) {
	probeLock.Lock()
	defer probeLock.Unlock()
	record.Hooks = append(record.Hooks, name)
}
