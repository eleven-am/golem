package p10operations

import (
	"context"
	"errors"
	"strings"
	"sync"

	golem "github.com/eleven-am/golem/go/golem"
	golemruntime "github.com/eleven-am/golem/go/runtime"
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

	ID     golem.UUID         `db:"id" golem:"id=p10operations.Invite.ID;pk"`
	TeamID golem.UUID         `db:"team_id"`
	Owner  string             `db:"owner" golem:"type=varchar(80)"`
	Email  string             `db:"email" golem:"type=varchar(160)"`
	Status string             `db:"status" golem:"type=varchar(40)"`
	Note   golem.Null[string] `db:"note" golem:"type=varchar(80)"`
	Team   *Team              `db:"-" golem:"relation=belongs_to;fields=team_id;references=id"`
}

func (Team) GolemModel() golem.ModelSpec[Team] {
	return golem.DefineModel(golem.Subscriptions[Team]())
}

type NoArgs struct{}

func (Team) DefineGraphQL(graphql *golem.GraphQLModel[Team]) {
	golem.ComputedField(graphql, "newestInvite", golem.GraphQLObject[Invite](), Team{}.NewestInvite, golem.Requires(Teams.Owner))
	golem.ComputedField(graphql, "pinnedInvite", golem.GraphQLObject[Invite]().NonNull(), Team{}.PinnedInvite, golem.Requires(Teams.Owner))
	golem.ComputedField(graphql, "openInvites", golem.GraphQLList(golem.GraphQLObject[Invite]().NonNull(), 20).NonNull(), Team{}.OpenInvites, golem.Requires(Teams.Owner))
}

func (Team) NewestInvite(ctx context.Context, row golem.Row[Team], _ NoArgs) (golem.Row[Invite], error) {
	rows, err := computedInvites(ctx, row)
	if err != nil || len(rows) == 0 {
		return golem.Row[Invite]{}, errors.Join(err, errors.New("team has no invite"))
	}
	return rows[0], nil
}

func (Team) PinnedInvite(ctx context.Context, row golem.Row[Team], arguments NoArgs) (golem.Row[Invite], error) {
	return Team{}.NewestInvite(ctx, row, arguments)
}

func (Team) OpenInvites(ctx context.Context, row golem.Row[Team], _ NoArgs) ([]golem.Row[Invite], error) {
	return computedInvites(ctx, row)
}

func computedInvites(ctx context.Context, row golem.Row[Team]) ([]golem.Row[Invite], error) {
	recordResolver("computedInvites")
	owner, _ := golem.Value(row, Teams.Owner).Get()
	probeLock.Lock()
	source := activeInviteSource
	probeLock.Unlock()
	if source == nil {
		return nil, nil
	}
	return source(ctx, owner)
}

func (Invite) GolemModel() golem.ModelSpec[Invite] {
	return golem.DefineModel(golem.Subscriptions[Invite]())
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
	rules.CanDelete(owned.And(Teams.Owner.StartsWith("al")))
	golem.Within(rules, InviteMember).CanDelete(owned)
}

func (Invite) DefinePolicy(rules *golem.Rules[Invite], actor Actor) {
	owned := Invites.Owner.Eq(actor.ID)
	rules.CanRead(owned)
	rules.CannotReadFields(Invites.Status.Eq("hidden-email"), Invites.Email)
	rules.CanUpdate(owned)
	rules.CannotUpdateFields(golem.All[Invite](), Invites.Status, Invites.Email)
	golem.Within(rules, InviteMember).CanCreateFields(owned, Invites.ID, Invites.TeamID, Invites.Owner, Invites.Email, Invites.Status, Invites.Team)
	golem.Within(rules, InviteMember).CanUpdateFields(owned, Invites.Status)
	golem.Within(rules, SubmitInvite).CanCreate(Invites.Status.Eq("submitted"))
	golem.Within(rules, SubmitInviteMaybe).CanCreate(Invites.Status.Eq("submitted"))
	golem.Within(rules, SubmitInvites).CanCreate(Invites.Status.Eq("submitted"))
	golem.Within(rules, SubmitCappedInvites).CanCreate(Invites.Status.Eq("submitted"))
	golem.Within(rules, SubmitBlank).CanCreate(Invites.Status.Eq("submitted"))
	golem.Within(rules, SubmitNested).CanCreate(Invites.Status.Eq("submitted"))
	golem.Within(rules, NestedTeamInvite).CanCreate(owned)
	golem.Within(rules, TransactionalInvite).CanCreate(owned)
	golem.Within(rules, UpsertInvite).CanCreate(owned)
	golem.Within(rules, UpsertInvite).CanUpdateFields(owned, Invites.Status)
	golem.Within(rules, AcceptInvite).CanUpdateFields(owned, Invites.Status)
	golem.Within(rules, ReviewInvite).CanCreateFields(owned, Invites.ID, Invites.Owner)
	golem.Within(rules, RecordInvite).CanCreate(owned)
	golem.Within(rules, MarkInvite).CanUpdateFields(owned, Invites.Status)
	if actor.Misconfigured {
		golem.Within(rules, SearchInvites).CanCreate(owned)
	}
}

func (Team) AfterCreate(ctx context.Context, result TeamCreateResult) error {
	recordHook("team_after_create")
	probeLock.Lock()
	hook := activeTeamHook
	probeLock.Unlock()
	if hook == nil {
		return nil
	}
	return hook(ctx, result.Executor())
}

func (Team) AfterFindMany(ctx context.Context, _ TeamFindManyResult) error {
	probeLock.Lock()
	hook := activeTeamReadHook
	probeLock.Unlock()
	if hook == nil {
		return nil
	}
	return hook(ctx)
}

func (Invite) BeforeCreate(ctx context.Context, _ *InviteCreateRequest) error {
	recordHook("before_create")
	return nil
}

func (Invite) AfterCreate(ctx context.Context, result InviteCreateResult) error {
	recordHook("after_create")
	probeLock.Lock()
	hook := activeInviteHook
	executorHook := activeInviteExecutorHook
	probeLock.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return err
		}
	}
	if executorHook == nil {
		return nil
	}
	return executorHook(ctx, result.Executor())
}

func (Invite) AfterUpdateMany(ctx context.Context, result InviteUpdateManyResult) error {
	recordHook("after_update_many")
	probeLock.Lock()
	hook := activeInviteManyHook
	executorHook := activeInviteManyExecutorHook
	probeLock.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return err
		}
	}
	if executorHook == nil {
		return nil
	}
	return executorHook(ctx, result.Executor())
}

func SetInviteUpdateManyExecutorHook(hook TeamHook) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeInviteManyExecutorHook = hook
}

func SetInviteUpdateManyHook(hook func(context.Context) error) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeInviteManyHook = hook
}

func SetInviteExecutorHook(hook TeamHook) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeInviteExecutorHook = hook
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

type SubmitArgs struct {
	ID     golem.UUID `golem:"graphql=id"`
	TeamID golem.UUID `golem:"graphql=teamID"`
	Owner  string     `golem:"graphql=owner"`
	Email  string     `golem:"graphql=email"`
}

type TeamArgs struct {
	ID    golem.UUID `golem:"graphql=id"`
	Owner string     `golem:"graphql=owner"`
}

type CappedSubmitArgs struct {
	First  golem.UUID `golem:"graphql=first"`
	Second golem.UUID `golem:"graphql=second"`
	Third  golem.UUID `golem:"graphql=third"`
	TeamID golem.UUID `golem:"graphql=teamID"`
}

type SubmitManyArgs struct {
	First   golem.UUID `golem:"graphql=first"`
	Second  golem.UUID `golem:"graphql=second"`
	Third   golem.UUID `golem:"graphql=third"`
	TeamID  golem.UUID `golem:"graphql=teamID"`
	Foreign string     `golem:"graphql=foreign"`
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "inviteMember", InviteMember)
	golem.Mutation(graphql, "acceptInvite", AcceptInvite)
	golem.Mutation(graphql, "reviewInvite", ReviewInvite)
	golem.Mutation(graphql, "nestedTeamInvite", NestedTeamInvite)
	golem.Mutation(graphql, "transactionalInvite", TransactionalInvite)
	golem.Mutation(graphql, "renewInvite", UpsertInvite)
	golem.Mutation(graphql, "submitInvite", SubmitInvite)
	golem.Mutation(graphql, "submitInviteMaybe", SubmitInviteMaybe)
	golem.Mutation(graphql, "submitInvites", SubmitInvites, golem.MaxItems(3))
	golem.Mutation(graphql, "submitCappedInvites", SubmitCappedInvites, golem.MaxItems(2))
	golem.Mutation(graphql, "submitBlank", SubmitBlank)
	golem.Mutation(graphql, "submitNested", SubmitNested)
	golem.Mutation(graphql, "submitTeam", SubmitTeam)
	golem.Query(graphql, "searchInvites", SearchInvites, golem.MaxItems(20))
	golem.Query(graphql, "cappedInvites", CappedInvites, golem.MaxItems(2))
	golem.Query(graphql, "firstInvite", FirstInvite)
	golem.Query(graphql, "requiredInvite", RequiredInvite)
	golem.Query(graphql, "searchTeams", SearchTeams, golem.MaxItems(20))
	golem.Query(graphql, "groupedInvites", GroupedInvites, golem.MaxItems(10, 2))
}

func DefineOperations(operations *golem.Operations) {
	golem.TxOperation(operations, "recordInvite", RecordInvite)
	golem.TxOperation(operations, "markInvite", MarkInvite)
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

func ReviewInvite(ctx context.Context, caller *Caller[Principal], _ AcceptArgs) (string, error) {
	recordResolver("reviewInvite")
	if err := runProbe(ctx, "reviewInvite", caller); err != nil {
		return "", err
	}
	return "reviewed", nil
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
	return inviteRows(ctx, caller, arguments.Owner)
}

func CappedInvites(ctx context.Context, caller *Caller[Principal], arguments SearchArgs) ([]golem.Row[Invite], error) {
	recordResolver("cappedInvites")
	return inviteRows(ctx, caller, arguments.Owner)
}

func FirstInvite(ctx context.Context, caller *Caller[Principal], arguments SearchArgs) (*golem.Row[Invite], error) {
	recordResolver("firstInvite")
	rows, err := inviteRows(ctx, caller, arguments.Owner)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func RequiredInvite(ctx context.Context, caller *Caller[Principal], arguments SearchArgs) (golem.Row[Invite], error) {
	recordResolver("requiredInvite")
	rows, err := inviteRows(ctx, caller, arguments.Owner)
	if err != nil {
		return golem.Row[Invite]{}, err
	}
	if len(rows) == 0 {
		return golem.Row[Invite]{}, errors.New("no invite for the owner")
	}
	return rows[0], nil
}

func GroupedInvites(ctx context.Context, caller *Caller[Principal], arguments SearchArgs) ([][]golem.Row[Invite], error) {
	recordResolver("groupedInvites")
	rows, err := inviteRows(ctx, caller, arguments.Owner)
	if err != nil {
		return nil, err
	}
	var groups [][]golem.Row[Invite]
	for start := 0; start < len(rows); start += 2 {
		end := start + 2
		if end > len(rows) {
			end = len(rows)
		}
		groups = append(groups, rows[start:end])
	}
	return groups, nil
}

func SearchTeams(ctx context.Context, caller *Caller[Principal], arguments SearchArgs) ([]golem.Row[Team], error) {
	recordResolver("searchTeams")
	return caller.Teams.FindMany(ctx, Teams.Where(Teams.Owner.Eq(arguments.Owner)), Teams.OrderBy(Teams.ID.Asc()), Teams.Take(20), Teams.Select(Teams.ID))
}

type InviteSource func(ctx context.Context, owner string) ([]golem.Row[Invite], error)

func SetInviteSource(source InviteSource) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeInviteSource = source
}

func inviteRows(ctx context.Context, caller *Caller[Principal], owner string) ([]golem.Row[Invite], error) {
	probeLock.Lock()
	source := activeInviteSource
	probeLock.Unlock()
	if source != nil {
		return source(ctx, owner)
	}
	return caller.Invites.FindMany(ctx, Invites.Where(Invites.Owner.Eq(owner)), Invites.OrderBy(Invites.ID.Asc()), Invites.Take(20), Invites.Select(Invites.ID, Invites.Status))
}

func submitted(ctx context.Context, caller *Caller[Principal], id, teamID golem.UUID, owner, email string) (golem.Row[Invite], error) {
	normalized, err := normalizedEmail(email)
	if err != nil {
		return golem.Row[Invite]{}, err
	}
	return caller.Invites.Create(ctx, Invites.Create(
		Invites.ID.Create(id),
		Invites.TeamID.Create(teamID),
		Invites.Owner.Create(owner),
		Invites.Email.Create(normalized),
		Invites.Status.Create("submitted"),
	), Invites.Select(Invites.ID, Invites.Status))
}

func SubmitInvite(ctx context.Context, caller *Caller[Principal], arguments SubmitArgs) (golem.Row[Invite], error) {
	recordResolver("submitInvite")
	return submitted(ctx, caller, arguments.ID, arguments.TeamID, arguments.Owner, arguments.Email)
}

func SubmitInviteMaybe(ctx context.Context, caller *Caller[Principal], arguments SubmitArgs) (*golem.Row[Invite], error) {
	recordResolver("submitInviteMaybe")
	row, err := submitted(ctx, caller, arguments.ID, arguments.TeamID, arguments.Owner, arguments.Email)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func SubmitInvites(ctx context.Context, caller *Caller[Principal], arguments SubmitManyArgs) ([]golem.Row[Invite], error) {
	recordResolver("submitInvites")
	var rows []golem.Row[Invite]
	for index, id := range []golem.UUID{arguments.First, arguments.Second, arguments.Third} {
		owner := "alpha"
		if index == 1 {
			owner = arguments.Foreign
		}
		row, err := submitted(ctx, caller, id, arguments.TeamID, owner, "batch@example.test")
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func SubmitCappedInvites(ctx context.Context, caller *Caller[Principal], arguments CappedSubmitArgs) ([]golem.Row[Invite], error) {
	recordResolver("submitCappedInvites")
	var rows []golem.Row[Invite]
	for _, id := range []golem.UUID{arguments.First, arguments.Second, arguments.Third} {
		row, err := submitted(ctx, caller, id, arguments.TeamID, "alpha", "capped@example.test")
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func SubmitNested(ctx context.Context, caller *Caller[Principal], arguments SubmitArgs) (golem.Row[Invite], error) {
	recordResolver("submitNested")
	email, err := normalizedEmail(arguments.Email)
	if err != nil {
		return golem.Row[Invite]{}, err
	}
	return caller.Invites.Create(ctx, Invites.Create(
		Invites.ID.Create(arguments.ID),
		Invites.Owner.Create(arguments.Owner),
		Invites.Email.Create(email),
		Invites.Status.Create("submitted"),
		Invites.Team.Create(Teams.Create(Teams.ID.Create(arguments.TeamID), Teams.Owner.Create("alpha"))),
	), Invites.Select(Invites.ID, Invites.Status))
}

func SubmitTeam(ctx context.Context, caller *Caller[Principal], arguments TeamArgs) (golem.Row[Team], error) {
	recordResolver("submitTeam")
	return caller.Teams.Create(ctx, Teams.Create(Teams.ID.Create(arguments.ID), Teams.Owner.Create(arguments.Owner)), Teams.Select(Teams.ID, Teams.Owner))
}

func SubmitBlank(ctx context.Context, caller *Caller[Principal], arguments SubmitArgs) (golem.Row[Invite], error) {
	recordResolver("submitBlank")
	if _, err := submitted(ctx, caller, arguments.ID, arguments.TeamID, arguments.Owner, arguments.Email); err != nil {
		return golem.Row[Invite]{}, err
	}
	return golem.RuntimeReadRow(GolemGeneratedInviteDescriptor)
}

func RecordInvite(ctx context.Context, tx *CallerTx[Principal], arguments InviteArgs) (string, error) {
	recordResolver("recordInvite")
	if err := runTxProbe(ctx, "recordInvite", tx); err != nil {
		return "", err
	}
	email, err := normalizedEmail(arguments.Email)
	if err != nil {
		return "", err
	}
	created, err := tx.Invites.Create(ctx, inviteInput(arguments, email), Invites.Select(Invites.ID))
	if err != nil {
		return "", err
	}
	id, _ := golem.Value(created, Invites.ID).Get()
	return id.String(), nil
}

func MarkInvite(ctx context.Context, tx *CallerTx[Principal], arguments AcceptArgs) (string, error) {
	recordResolver("markInvite")
	if err := runTxProbe(ctx, "markInvite", tx); err != nil {
		return "", err
	}
	updated, err := tx.Invites.Update(ctx, Invites.ByID.Value(arguments.ID), Invites.Update(Invites.Status.Set("marked")), Invites.Select(Invites.Status))
	if err != nil {
		return "", err
	}
	status, _ := golem.Value(updated, Invites.Status).Get()
	return status, nil
}

func RunCustomMutationAsTxOperation(ctx context.Context, tx *CallerTx[Principal], arguments InviteArgs) (string, error) {
	var inner *golemruntime.CallerTx[Principal, Actor]
	if tx != nil {
		inner = tx.runtime
	}
	return golemruntime.RunCallerTxOperation(ctx, inner, InviteMember, func(*golemruntime.CallerTx[Principal, Actor]) *Caller[Principal] { return nil }, arguments)
}

func MutateTxOperation(ctx context.Context, caller *Caller[Principal], arguments InviteArgs) (string, error) {
	var inner *golemruntime.Caller[Principal, Actor]
	if caller != nil {
		inner = caller.runtime
	}
	return golemruntime.RunCallerOperation(ctx, inner, RecordInvite, func(*golemruntime.Caller[Principal, Actor]) *CallerTx[Principal] { return nil }, arguments)
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

type TeamHook func(ctx context.Context, executor golem.HookExecutor) error

type TxProbe func(ctx context.Context, operation string, tx *CallerTx[Principal]) error

func SetTxProbe(probe TxProbe) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeTxProbe = probe
}

func SetTeamHook(hook TeamHook) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeTeamHook = hook
}

func SetTeamReadHook(hook func(context.Context) error) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeTeamReadHook = hook
}

func SetInviteHook(hook func(context.Context) error) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeInviteHook = hook
}

type Record struct {
	Resolvers []string
	Hooks     []string
}

var (
	probeLock                    sync.Mutex
	activeProbe                  Probe
	activeTxProbe                TxProbe
	activeTeamHook               TeamHook
	activeTeamReadHook           func(context.Context) error
	activeInviteHook             func(context.Context) error
	activeInviteExecutorHook     TeamHook
	activeInviteManyHook         func(context.Context) error
	activeInviteManyExecutorHook TeamHook
	activeInviteSource           InviteSource
	record                       Record
)

func Reset(probe Probe) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeProbe = probe
	activeTxProbe = nil
	activeTeamHook = nil
	activeTeamReadHook = nil
	activeInviteHook = nil
	activeInviteExecutorHook = nil
	activeInviteManyHook = nil
	activeInviteManyExecutorHook = nil
	activeInviteSource = nil
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

func runTxProbe(ctx context.Context, operation string, tx *CallerTx[Principal]) error {
	probeLock.Lock()
	probe := activeTxProbe
	probeLock.Unlock()
	if probe == nil {
		return nil
	}
	return probe(ctx, operation, tx)
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
