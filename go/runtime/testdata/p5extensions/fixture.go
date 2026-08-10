package p5extensions

import (
	"context"
	"errors"
	"fmt"
	"sync"

	golem "github.com/eleven-am/golem/go/golem"
)

type Principal struct {
	ID    string
	Valid bool
}

type Actor struct{ ID string }

type User struct {
	_ struct{} `golem:"model;id=p5extensions.User;table=users;graphql=User"`

	ID      golem.UUID `db:"id" golem:"id=p5extensions.User.ID;pk"`
	Owner   string     `db:"owner" golem:"type=varchar(80)"`
	Name    string     `db:"name" golem:"type=varchar(120)"`
	Counter int32      `db:"counter" golem:"default=0"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "p5_extensions")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	owned := Users.Owner.Eq(actor.ID)
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
	rules.CanReadFields(owned, Users.ID, Users.Owner, Users.Counter)
	rules.CannotReadFields(owned, Users.Name)
	rules.CanReadFields(owned.And(Users.Name.EndsWith("-open")), Users.Name)
	rules.CanCreateFields(owned, Users.ID, Users.Owner, Users.Name, Users.Counter)
	rules.CanUpdateFields(owned, Users.Name, Users.Counter)
}

type GreetingArgs struct {
	Prefix string `golem:"graphql=prefix"`
}

func (User) DefineGraphQL(graphql *golem.GraphQLModel[User]) {
	golem.ComputedField(graphql, "greeting", golem.GraphQLString().NonNull(), User{}.Greeting, golem.Requires(Users.Name))
	golem.BatchedComputedFieldWithCacheKey(graphql, "batchGreeting", golem.GraphQLString().NonNull(), Users.ID, LoadGreetings, GreetingCacheKey, 2, golem.Requires(Users.Name))
}

func (User) Greeting(_ context.Context, row golem.Row[User], arguments GreetingArgs) (string, error) {
	name, present := golem.Value(row, Users.Name).Get()
	if !present {
		return arguments.Prefix + ":masked", nil
	}
	return arguments.Prefix + ":" + name, nil
}

func LoadGreetings(ctx context.Context, keys []golem.UUID, arguments GreetingArgs) (map[golem.UUID]string, error) {
	probeLock.Lock()
	probeState.BatchSizes = append(probeState.BatchSizes, len(keys))
	probeState.BatchPrefixes = append(probeState.BatchPrefixes, arguments.Prefix)
	started, blocked := probeState.loadStarted, probeState.loadBlocked
	probeState.loadStarted, probeState.loadBlocked = nil, nil
	probeLock.Unlock()
	if started != nil {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-blocked:
		}
	}
	result := make(map[golem.UUID]string, len(keys))
	for _, key := range keys {
		result[key] = arguments.Prefix + ":" + key.String()
	}
	return result, nil
}

func GreetingCacheKey(value golem.UUID) (string, error) { return value.String(), nil }

type SearchUsersArgs struct {
	Where golem.Predicate[User] `golem:"graphql=where"`
}

type TransactionalUserArgs struct {
	ID    golem.UUID `golem:"graphql=id"`
	Owner string     `golem:"graphql=owner"`
	Name  string     `golem:"graphql=name"`
	Fail  bool       `golem:"graphql=fail"`
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "searchUsers", SearchUsers)
	golem.Mutation(graphql, "transactionalUser", TransactionalUser)
}

func SearchUsers(ctx context.Context, caller *Caller[Principal], arguments SearchUsersArgs) ([]golem.Row[User], error) {
	recordCustomCaller(caller)
	return caller.Users.FindMany(ctx,
		Users.Where(arguments.Where),
		Users.OrderBy(Users.ID.Asc()),
		Users.Take(10),
		Users.Select(Users.ID, Users.Owner, Users.Name, Users.Counter),
	)
}

func TransactionalUser(ctx context.Context, caller *Caller[Principal], arguments TransactionalUserArgs) (golem.Row[User], error) {
	recordCustomCaller(caller)
	probeLock.Lock()
	probeState.TransactionInvocations++
	probeLock.Unlock()
	var result golem.Row[User]
	err := caller.Transaction(ctx, func(transaction *CallerTx[Principal]) error {
		probeLock.Lock()
		probeState.TransactionCallbacks++
		probeLock.Unlock()
		created, createErr := transaction.Users.Create(ctx,
			Users.Create(
				Users.ID.Create(arguments.ID),
				Users.Owner.Create(arguments.Owner),
				Users.Name.Create(arguments.Name),
			),
			Users.Select(Users.ID, Users.Owner, Users.Name, Users.Counter),
		)
		if createErr != nil {
			return createErr
		}
		result = created
		if arguments.Fail {
			return errors.New("requested custom transaction rollback")
		}
		return nil
	})
	return result, err
}

type Probe struct {
	BatchSizes             []int
	BatchPrefixes          []string
	CustomCallers          []string
	TransactionInvocations int
	TransactionCallbacks   int
	loadStarted            chan struct{}
	loadBlocked            chan struct{}
}

var (
	probeLock  sync.Mutex
	probeState Probe
)

func ResetProbe() {
	probeLock.Lock()
	defer probeLock.Unlock()
	if probeState.loadBlocked != nil {
		close(probeState.loadBlocked)
	}
	probeState = Probe{}
}

func SnapshotProbe() Probe {
	probeLock.Lock()
	defer probeLock.Unlock()
	result := probeState
	result.BatchSizes = append([]int(nil), probeState.BatchSizes...)
	result.BatchPrefixes = append([]string(nil), probeState.BatchPrefixes...)
	result.CustomCallers = append([]string(nil), probeState.CustomCallers...)
	result.loadStarted, result.loadBlocked = nil, nil
	return result
}

func ArmNextLoad() <-chan struct{} {
	probeLock.Lock()
	defer probeLock.Unlock()
	if probeState.loadBlocked != nil {
		close(probeState.loadBlocked)
	}
	probeState.loadStarted = make(chan struct{})
	probeState.loadBlocked = make(chan struct{})
	return probeState.loadStarted
}

func ReleaseLoad() {
	probeLock.Lock()
	defer probeLock.Unlock()
	if probeState.loadBlocked != nil {
		close(probeState.loadBlocked)
		probeState.loadBlocked = nil
		probeState.loadStarted = nil
	}
}

func recordCustomCaller(caller *Caller[Principal]) {
	probeLock.Lock()
	defer probeLock.Unlock()
	probeState.CustomCallers = append(probeState.CustomCallers, fmt.Sprintf("%p", caller))
}
