package runtime_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

var errP10StopOperation = errors.New("operation stopped by the test")

type p10CommitGate struct {
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	open    chan struct{}
}

func (gate *p10CommitGate) arm() (entered <-chan struct{}, open func()) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.armed = true
	gate.entered = make(chan struct{})
	gate.open = make(chan struct{})
	release := gate.open
	var once sync.Once
	return gate.entered, func() { once.Do(func() { close(release) }) }
}

func (gate *p10CommitGate) wait() {
	gate.mu.Lock()
	if !gate.armed {
		gate.mu.Unlock()
		return
	}
	gate.armed = false
	entered, open := gate.entered, gate.open
	gate.mu.Unlock()
	close(entered)
	<-open
}

type p10GateConnector struct {
	base driver.Connector
	gate *p10CommitGate
}

func (connector p10GateConnector) Connect(ctx context.Context) (driver.Conn, error) {
	connection, err := connector.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &p10GateConn{p5ExtensionTraceConn: &p5ExtensionTraceConn{Conn: connection, trace: &p5ExtensionSQLTrace{}}, gate: connector.gate}, nil
}

func (connector p10GateConnector) Driver() driver.Driver { return connector.base.Driver() }

type p10GateConn struct {
	*p5ExtensionTraceConn
	gate *p10CommitGate
}

func (connection *p10GateConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	transaction, err := connection.p5ExtensionTraceConn.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return p10GateTx{Tx: transaction, gate: connection.gate}, nil
}

type p10GateTx struct {
	driver.Tx
	gate *p10CommitGate
}

func (transaction p10GateTx) Commit() error {
	transaction.gate.wait()
	return transaction.Tx.Commit()
}

func openP10OperationDatabase(t *testing.T, profile p5ExtensionProviderProfile, gate *p10CommitGate) *sqlx.DB {
	t.Helper()
	ctx := context.Background()
	var database *sqlx.DB
	if profile.provider == golem.SQLite {
		plainDSN := "file:" + filepath.Join(t.TempDir(), "operations.sqlite")
		bootstrap, _, err := sqliteprovider.New().Open(ctx, plainDSN)
		if err != nil {
			t.Fatal(err)
		}
		registeredDriver := bootstrap.Driver()
		if err := bootstrap.Close(); err != nil {
			t.Fatal(err)
		}
		base := p5ExtensionDriverConnector{driver: registeredDriver, dsn: plainDSN + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"}
		database = sqlx.NewDb(sql.OpenDB(p10GateConnector{base: base, gate: gate}), "sqlite")
	} else {
		configuration, err := pgx.ParseConfig(testenv.DisposablePostgreSQL(t, profile.env))
		if err != nil {
			t.Fatal(err)
		}
		if configuration.RuntimeParams == nil {
			configuration.RuntimeParams = map[string]string{}
		}
		configuration.RuntimeParams["timezone"] = "UTC"
		configuration.RuntimeParams["datestyle"] = "ISO, YMD"
		configuration.RuntimeParams["intervalstyle"] = "iso_8601"
		configuration.RuntimeParams["standard_conforming_strings"] = "on"
		database = sqlx.NewDb(sql.OpenDB(p10GateConnector{base: stdlib.GetConnector(*configuration), gate: gate}), "pgx")
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func (fixture *p10OperationFixture) holdConnectionPool(t *testing.T) (waiting func() bool, release func()) {
	t.Helper()
	fixture.database.SetMaxOpenConns(1)
	fixture.database.SetMaxIdleConns(1)
	connection, err := fixture.database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := fixture.database.Stats().WaitCount
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = connection.Close()
			fixture.database.SetMaxOpenConns(4)
			fixture.database.SetMaxIdleConns(4)
		})
	}
	t.Cleanup(release)
	return func() bool { return fixture.database.Stats().WaitCount > before }, release
}

func p10AwaitCondition(condition func() bool) {
	for !condition() {
		goruntime.Gosched()
	}
}

func p10OperationReleaseIsWaiting() bool {
	buffer := make([]byte, 1<<22)
	stacks := string(buffer[:goruntime.Stack(buffer, true)])
	for _, stack := range strings.Split(stacks, "\n\n") {
		if strings.Contains(stack, "operationScope).release") && strings.Contains(stack, "sync.(*Mutex).Lock") {
			return true
		}
	}
	return false
}

func p10BlockAfterCreate() (reached func() bool, proceed func()) {
	arrived := make(chan struct{})
	released := make(chan struct{})
	var first, release sync.Once
	p10operations.SetInviteHook(func(context.Context) error {
		blocking := false
		first.Do(func() { blocking = true })
		if blocking {
			close(arrived)
			<-released
		}
		return nil
	})
	return func() bool {
			select {
			case <-arrived:
				return true
			default:
				return false
			}
		}, func() {
			release.Do(func() { close(released) })
		}
}

type p10LeaseWrite struct {
	name    string
	prepare func(*testing.T, *p10OperationFixture)
	hook    func(*testing.T, *p10OperationFixture)
	inHook  bool
	write   func(context.Context, *p10OperationFixture, *p10operations.Caller[p10operations.Principal]) error
	absent  func(*testing.T, *p10OperationFixture) bool
}

func p10LeaseWrites(t *testing.T) []p10LeaseWrite {
	created := p10OperationID(t, 900)
	return []p10LeaseWrite{
		{
			name: "scalar create",
			write: func(ctx context.Context, fixture *p10OperationFixture, caller *p10operations.Caller[p10operations.Principal]) error {
				return fixture.genericCreate(ctx, caller, created)
			},
			absent: func(t *testing.T, fixture *p10OperationFixture) bool {
				exists, _, _ := fixture.inviteExists(t, created)
				return !exists
			},
		},
		{
			name:   "upsert",
			inHook: true,
			write: func(ctx context.Context, fixture *p10OperationFixture, caller *p10operations.Caller[p10operations.Principal]) error {
				_, err := caller.Invites.Upsert(ctx, p10operations.Invites.ByID.Value(created),
					p10operations.Invites.Create(p10operations.Invites.ID.Create(created), p10operations.Invites.TeamID.Create(fixture.alpha), p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("u@example.test"), p10operations.Invites.Status.Create("pending")),
					p10operations.Invites.Update(p10operations.Invites.Status.Set("renewed")),
				)
				return err
			},
			absent: func(t *testing.T, fixture *p10OperationFixture) bool {
				exists, _, _ := fixture.inviteExists(t, created)
				return !exists
			},
		},
		{
			name:   "nested create",
			inHook: true,
			write: func(ctx context.Context, fixture *p10OperationFixture, caller *p10operations.Caller[p10operations.Principal]) error {
				_, err := caller.Teams.Create(ctx, p10operations.Teams.Create(
					p10operations.Teams.ID.Create(p10OperationID(t, 901)), p10operations.Teams.Owner.Create("alpha"),
					p10operations.Teams.Invites.Create(p10operations.Invites.Create(
						p10operations.Invites.ID.Create(created), p10operations.Invites.Owner.Create("alpha"),
						p10operations.Invites.Email.Create("nested@example.test"), p10operations.Invites.Status.Create("pending"),
					)),
				))
				return err
			},
			absent: func(t *testing.T, fixture *p10OperationFixture) bool {
				exists, _, _ := fixture.inviteExists(t, created)
				_, teamErr := fixture.app.System().Teams.FindUnique(context.Background(), p10operations.Teams.ByID.Value(p10OperationID(t, 901)))
				return !exists && teamErr != nil
			},
		},
		{
			name:    "batch update",
			prepare: func(t *testing.T, fixture *p10OperationFixture) { fixture.seedInvite(t, created) },
			write: func(ctx context.Context, fixture *p10OperationFixture, caller *p10operations.Caller[p10operations.Principal]) error {
				_, err := caller.Invites.UpdateMany(ctx, p10operations.Invites.ID.Eq(created), p10operations.Invites.UpdateMany(p10operations.Invites.Status.Set("accepted")))
				return err
			},
			absent: func(t *testing.T, fixture *p10OperationFixture) bool {
				_, _, status := fixture.inviteExists(t, created)
				return status == "pending"
			},
		},
		{
			name:   "hook executor",
			inHook: true,
			hook: func(t *testing.T, fixture *p10OperationFixture) {
				p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
					_, err := golem.HookCreateRow(ctx, executor, p10operations.GolemGeneratedInviteDescriptor, p10operations.Invites.Create(
						p10operations.Invites.ID.Create(created), p10operations.Invites.TeamID.Create(p10OperationID(t, 902)),
						p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("hook@example.test"), p10operations.Invites.Status.Create("pending"),
					))
					return err
				})
			},
			write: func(ctx context.Context, fixture *p10OperationFixture, caller *p10operations.Caller[p10operations.Principal]) error {
				_, err := caller.Teams.Create(ctx, p10operations.Teams.Create(p10operations.Teams.ID.Create(p10OperationID(t, 902)), p10operations.Teams.Owner.Create("alpha")))
				return err
			},
			absent: func(t *testing.T, fixture *p10OperationFixture) bool {
				exists, _, _ := fixture.inviteExists(t, created)
				_, teamErr := fixture.app.System().Teams.FindUnique(context.Background(), p10operations.Teams.ByID.Value(p10OperationID(t, 902)))
				return !exists && teamErr != nil
			},
		},
	}
}

func TestOperationWritePlannedBeforeTheOperationEndsCannotCommitAfterItAcrossProviders(t *testing.T) {
	for _, write := range p10LeaseWrites(t) {
		write := write
		t.Run(write.name, func(t *testing.T) {
			forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
				ctx := context.Background()
				if write.prepare != nil {
					write.prepare(t, fixture)
				}
				if write.hook != nil {
					write.hook(t, fixture)
				}
				caller := fixture.caller(t, "alpha")
				usual := write.write(ctx, fixture, caller)
				if usual == nil {
					t.Fatal("the caller performed a write only the operation may perform")
				}
				blocked, proceed := func() bool { return false }, func() {}
				if !write.inHook {
					blocked, proceed = fixture.holdConnectionPool(t)
				}
				late := make(chan error, 1)
				p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
					if operation != "inviteMember" {
						return nil
					}
					go func() { late <- write.write(context.Background(), fixture, scoped) }()
					p10AwaitCondition(blocked)
					return errP10StopOperation
				})
				if write.inHook {
					blocked, proceed = p10BlockAfterCreate()
				}
				if write.hook != nil {
					write.hook(t, fixture)
				}
				if _, err := p10operations.Mutate(ctx, caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 999), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
					t.Fatalf("operation = %v", err)
				}
				proceed()
				assertP10SameRefusal(t, write.name+" planned inside the operation", usual, <-late)
				if !write.absent(t, fixture) {
					t.Fatalf("%s authorised by the operation committed after it ended", write.name)
				}
			})
		})
	}
}

func TestOperationTransactionCannotCommitAfterTheOperationEndsAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		created := p10OperationID(t, 910)
		usual := caller.Transaction(ctx, func(transaction *p10operations.CallerTx[p10operations.Principal]) error {
			_, err := transaction.Invites.Create(ctx, p10operations.Invites.Create(
				p10operations.Invites.ID.Create(created), p10operations.Invites.TeamID.Create(fixture.alpha),
				p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("tx@example.test"), p10operations.Invites.Status.Create("pending"),
			))
			return err
		})
		if usual == nil {
			t.Fatal("the caller's own transaction created a closed model")
		}
		written := make(chan struct{})
		proceed := make(chan struct{})
		late := make(chan error, 1)
		p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
			if operation != "inviteMember" {
				return nil
			}
			go func() {
				late <- scoped.Transaction(context.Background(), func(transaction *p10operations.CallerTx[p10operations.Principal]) error {
					if _, err := transaction.Invites.Create(context.Background(), p10operations.Invites.Create(
						p10operations.Invites.ID.Create(created), p10operations.Invites.TeamID.Create(fixture.alpha),
						p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("tx@example.test"), p10operations.Invites.Status.Create("pending"),
					)); err != nil {
						return err
					}
					close(written)
					<-proceed
					return nil
				})
			}()
			<-written
			return errP10StopOperation
		})
		if _, err := p10operations.Mutate(ctx, caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 911), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
			t.Fatalf("operation = %v", err)
		}
		close(proceed)
		assertP10SameRefusal(t, "transaction committing after the operation", usual, <-late)
		if exists, _, _ := fixture.inviteExists(t, created); exists {
			t.Fatal("a transaction committed a write the ended operation authorised")
		}
	})
}

func TestOperationCommitInProgressWhenTheOperationEndsCompletesAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		created := p10OperationID(t, 920)
		entered, open := fixture.gate.arm()
		defer open()
		late := make(chan error, 1)
		p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
			if operation != "inviteMember" {
				return nil
			}
			go func() { late <- fixture.genericCreate(context.Background(), scoped, created) }()
			<-entered
			return errP10StopOperation
		})
		done := make(chan error, 1)
		go func() {
			_, err := p10operations.Mutate(ctx, caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 921), "stop@example.test"))
			done <- err
		}()
		var ended error
		endedEarly := false
		p10AwaitCondition(func() bool {
			select {
			case ended = <-done:
				endedEarly = true
				return true
			default:
				return p10OperationReleaseIsWaiting()
			}
		})
		if endedEarly {
			t.Fatalf("the operation ended while a commit it authorised was in progress: %v", ended)
		}
		open()
		if err := <-late; err != nil {
			t.Fatalf("a commit in progress when the operation ended was refused: %v", err)
		}
		if err := <-done; !errors.Is(err, errP10StopOperation) {
			t.Fatalf("operation = %v", err)
		}
		if exists, _, _ := fixture.inviteExists(t, created); !exists {
			t.Fatal("the commit in progress when the operation ended did not persist")
		}
	})
}
