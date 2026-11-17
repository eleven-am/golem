package boundedmaintenance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queue"
)

type Principal struct{ OwnerID golem.UUID }
type Actor struct{ OwnerID golem.UUID }

// A removal generation survives physical root removal in a separate task.
// Queue payloads identify one expected step; they never carry a trusted cursor,
// caller authority, a database handle, or an unbounded list of descendants.
type CleanupPage struct {
	TaskID     golem.UUID
	Generation golem.UUID
	Sequence   int32
}

const CleanupLeafPageSize = 32

type Show struct {
	_        struct{}   `golem:"model;table=maintenance_shows"`
	ID       golem.UUID `db:"id" golem:"pk"`
	OwnerID  golem.UUID `db:"owner_id"`
	Removed  bool       `db:"removed"`
	Fence    int64      `db:"fence"`
	Episodes []Episode  `db:"-" golem:"relation=has_many;fields=id;references=show_id"`
}

type Episode struct {
	_      struct{}   `golem:"model;table=maintenance_episodes"`
	_      struct{}   `golem:"index=maintenance_episodes_show(show_id,id)"`
	ID     golem.UUID `db:"id" golem:"pk"`
	ShowID golem.UUID `db:"show_id"`
	Title  string     `db:"title" golem:"type=varchar(1024)"`
	Show   *Show      `db:"-" golem:"relation=belongs_to;fields=show_id;references=id"`
	Assets []Asset    `db:"-" golem:"relation=has_many;fields=id;references=episode_id"`
}

type Asset struct {
	_         struct{}   `golem:"model;table=maintenance_assets"`
	_         struct{}   `golem:"index=maintenance_assets_episode(episode_id,id)"`
	ID        golem.UUID `db:"id" golem:"pk"`
	EpisodeID golem.UUID `db:"episode_id"`
	Body      string     `db:"body" golem:"type=varchar(1024)"`
	Episode   *Episode   `db:"-" golem:"relation=belongs_to;fields=episode_id;references=id"`
}

type RemovalTask struct {
	_          struct{}   `golem:"model;table=maintenance_tasks"`
	ID         golem.UUID `db:"id" golem:"pk"`
	ShowID     golem.UUID `db:"show_id" golem:"unique"`
	OwnerID    golem.UUID `db:"owner_id"`
	Generation golem.UUID `db:"generation"`
	Sequence   int32      `db:"sequence"`
	Phase      int32      `db:"phase"`
	Deleted    int32      `db:"deleted"`
	Completed  bool       `db:"completed"`
	// ShowID intentionally has no FK: this record fences stale jobs after the
	// root is gone. Reusing root identities is not part of this workflow.
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "bounded_maintenance")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[Show](schema)
	golem.Model[Episode](schema)
	golem.Model[Asset](schema)
	golem.Model[RemovalTask](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (Show) GolemModel() golem.ModelSpec[Show] {
	return golem.DefineModel(golem.Subscriptions[Show]())
}
func (Episode) GolemModel() golem.ModelSpec[Episode] {
	return golem.DefineModel(
		golem.Subscriptions[Episode](golem.DeleteSnapshot(Episodes.ShowID, Episodes.Title)),
		golem.RelationOptions(Episodes.Show).OnDelete(golem.Cascade),
	)
}
func (Asset) GolemModel() golem.ModelSpec[Asset] {
	return golem.DefineModel(
		golem.Subscriptions[Asset](golem.DeleteSnapshot(Assets.EpisodeID, Assets.Body)),
		golem.RelationOptions(Assets.Episode).OnDelete(golem.Cascade),
	)
}
func (RemovalTask) GolemModel() golem.ModelSpec[RemovalTask] {
	return golem.DefineModel(golem.Subscriptions[RemovalTask]())
}

func (Show) DefinePolicy(rules *golem.Rules[Show], actor Actor) {
	owner := Shows.OwnerID.Eq(actor.OwnerID)
	rules.CanRead(owner.And(Shows.Removed.Eq(false)))
	rules.CanCreate(owner)
	rules.CanUpdate(owner)
	rules.CanDelete(owner)
}
func (Episode) DefinePolicy(rules *golem.Rules[Episode], actor Actor) {
	live := Episodes.Show.Is(Shows.OwnerID.Eq(actor.OwnerID).And(Shows.Removed.Eq(false)))
	rules.CanRead(live)
	rules.CanCreate(live)
	rules.CanUpdate(live)
}
func (Asset) DefinePolicy(rules *golem.Rules[Asset], actor Actor) {
	// Private physical descendants still count toward mutation/event budgets.
	rules.CanRead(golem.None[Asset]())
	live := Assets.Episode.Is(Episodes.Show.Is(Shows.OwnerID.Eq(actor.OwnerID).And(Shows.Removed.Eq(false))))
	rules.CanCreate(live)
	rules.CanUpdate(live)
}
func (RemovalTask) DefinePolicy(rules *golem.Rules[RemovalTask], actor Actor) {
	owner := RemovalTasks.OwnerID.Eq(actor.OwnerID)
	rules.CanRead(owner)
	rules.CanCreate(owner)
}

var ErrRootUnavailable = errors.New("maintenance root is absent, removed, or unavailable")
var ErrFenceChanged = errors.New("maintenance transaction must retry from its root fence")

const CleanupJobType = "maintenance.cleanup-page"

// Maintenance is application-owned wiring, assigned once after Open and before
// a worker starts. Registering it does not start workers or perform database work.
type Maintenance struct {
	Application *App[Principal]
	pages       queue.Type[CleanupPage]
}

func RegisterMaintenance(registry *queue.Registry) (*Maintenance, error) {
	service := &Maintenance{}
	pages, err := queue.Register(registry, queue.Definition[CleanupPage]{
		Type: CleanupJobType, MaxAttempts: 5, Timeout: 10 * time.Second,
		Backoff:     queue.Backoff{Base: 10 * time.Millisecond, Cap: time.Second},
		ExclusiveBy: func(page CleanupPage) string { return page.TaskID.String() },
		Handle: func(ctx context.Context, job queue.Job[CleanupPage]) error {
			return service.ProcessPage(ctx, job.Payload)
		},
	})
	if err != nil {
		return nil, err
	}
	service.pages = pages
	return service, nil
}

func (service *Maintenance) pending(page CleanupPage) (queue.Pending, error) {
	// A root-wide dedupe key would coalesce the successor into the still-leased
	// current job. Each committed step owns a distinct durable key instead.
	return service.pages.New(page, queue.Dedupe(fmt.Sprintf("cleanup:%s:%s:%d", page.TaskID, page.Generation, page.Sequence)))
}

type showFenceClient interface {
	FindMany(context.Context, ...golem.ReadOption[Show]) ([]golem.Row[Show], error)
	UpdateMany(context.Context, golem.Predicate[Show], golem.UpdateManyInput[Show]) (int64, error)
}

// fenceShow is shared by caller removal, caller/System ingest and System
// cleanup. A real compare-and-write fences snapshots and concurrent writers;
// advisory keys alone would not refresh a snapshot after waiting, and are a
// deliberate no-op on SQLite. Callers must hold this fence until commit.
func fenceShow(ctx context.Context, client showFenceClient, id golem.UUID, removed, nextRemoved bool) (golem.UUID, error) {
	rows, err := client.FindMany(ctx, golem.Where(Shows.ID.Eq(id).And(Shows.Removed.Eq(removed))), golem.Select[Show](Shows.OwnerID, Shows.Fence), golem.Take[Show](1))
	if err != nil {
		return golem.UUID{}, err
	}
	if len(rows) != 1 {
		return golem.UUID{}, ErrRootUnavailable
	}
	owner, ownerOK := golem.Value(rows[0], Shows.OwnerID).Get()
	fence, fenceOK := golem.Value(rows[0], Shows.Fence).Get()
	if !ownerOK || !fenceOK || fence == math.MaxInt64 {
		return golem.UUID{}, fmt.Errorf("maintenance root fence is unavailable or exhausted")
	}
	count, err := client.UpdateMany(ctx, Shows.ID.Eq(id).And(Shows.Removed.Eq(removed)).And(Shows.Fence.Eq(fence)), Shows.UpdateMany(Shows.Fence.Set(fence+1), Shows.Removed.Set(nextRemoved)))
	if err != nil {
		return golem.UUID{}, err
	}
	if count != 1 {
		return golem.UUID{}, ErrFenceChanged
	}
	return owner, nil
}

func maintenanceLock(id golem.UUID) golem.LockKey {
	return golem.MustLockKey("maintenance-root", id.String())
}

// RequestRemoval commits logical invisibility, a durable generation and its
// first job together. IDs are supplied by the command owner before retryable
// work begins; retries never generate another task or perform external effects.
func (service *Maintenance) RequestRemoval(ctx context.Context, caller *Caller[Principal], showID, taskID, generation golem.UUID) error {
	if service == nil || caller == nil || showID == (golem.UUID{}) || taskID == (golem.UUID{}) || generation == (golem.UUID{}) {
		return fmt.Errorf("maintenance removal identities and caller are required")
	}
	return caller.Transaction(ctx, func(tx *CallerTx[Principal]) error {
		return service.RequestRemovalIntoTx(ctx, tx, showID, taskID, generation)
	}, golem.RetryConflicts(3))
}

// RequestRemovalIntoTx composes the same command with other application work.
// The outer owner controls commit/retry; no effects escape its transaction.
func (service *Maintenance) RequestRemovalIntoTx(ctx context.Context, tx *CallerTx[Principal], showID, taskID, generation golem.UUID) error {
	if service == nil || tx == nil || showID == (golem.UUID{}) || taskID == (golem.UUID{}) || generation == (golem.UUID{}) {
		return fmt.Errorf("maintenance removal identities and transaction are required")
	}
	if err := tx.LockKeys(ctx, maintenanceLock(showID)); err != nil {
		return err
	}
	existing, err := tx.RemovalTasks.FindMany(ctx, golem.Where(RemovalTasks.ShowID.Eq(showID)), golem.Take[RemovalTask](1))
	if err != nil || len(existing) != 0 {
		return err // An admitted task is the idempotent result, even after completion.
	}
	owner, err := fenceShow(ctx, tx.Shows, showID, false, true)
	if err != nil {
		return err
	}
	if _, err := tx.RemovalTasks.Create(ctx, RemovalTasks.Create(RemovalTasks.ID.Create(taskID), RemovalTasks.ShowID.Create(showID), RemovalTasks.OwnerID.Create(owner), RemovalTasks.Generation.Create(generation), RemovalTasks.Sequence.Create(0), RemovalTasks.Phase.Create(0), RemovalTasks.Deleted.Create(0), RemovalTasks.Completed.Create(false))); err != nil {
		return err
	}
	pending, err := service.pending(CleanupPage{TaskID: taskID, Generation: generation})
	if err != nil {
		return err
	}
	_, err = tx.Enqueue(ctx, pending)
	return err
}

type episodeWriter interface {
	FindMany(context.Context, ...golem.ReadOption[Episode]) ([]golem.Row[Episode], error)
	Create(context.Context, golem.CreateInput[Episode], ...golem.Projection[Episode]) (golem.Row[Episode], error)
	UpdateMany(context.Context, golem.Predicate[Episode], golem.UpdateManyInput[Episode]) (int64, error)
}

type IngestEpisode struct {
	ID, ShowID golem.UUID
	Title      string
}

// Both public entry points use the same invariant. System does not run caller
// hooks, so correctness cannot live solely in a caller BeforeCreate hook.
func IngestCaller(ctx context.Context, caller *Caller[Principal], input IngestEpisode) error {
	return caller.Transaction(ctx, func(tx *CallerTx[Principal]) error {
		return IngestCallerIntoTx(ctx, tx, input)
	}, golem.RetryConflicts(3))
}

func IngestSystem(ctx context.Context, application *App[Principal], input IngestEpisode) error {
	return application.System().Transaction(ctx, func(tx *SystemTx[Principal]) error {
		return IngestSystemIntoTx(ctx, tx, input)
	}, golem.RetryConflicts(3))
}

// The IntoTx forms retain the same authority and fence owner as standalone
// commands, and allow an application-owned transaction to compose commands.
func IngestCallerIntoTx(ctx context.Context, tx *CallerTx[Principal], input IngestEpisode) error {
	if tx == nil {
		return fmt.Errorf("ingest caller transaction is required")
	}
	return ingestEpisode(ctx, tx.Shows, tx.Episodes, tx.LockKeys, input)
}

func IngestSystemIntoTx(ctx context.Context, tx *SystemTx[Principal], input IngestEpisode) error {
	if tx == nil {
		return fmt.Errorf("ingest system transaction is required")
	}
	return ingestEpisode(ctx, tx.Shows, tx.Episodes, tx.LockKeys, input)
}

func ingestEpisode(ctx context.Context, shows showFenceClient, episodes episodeWriter, lock func(context.Context, ...golem.LockKey) error, input IngestEpisode) error {
	if input.ID == (golem.UUID{}) || input.ShowID == (golem.UUID{}) {
		return fmt.Errorf("ingest identities are required")
	}
	rows, err := episodes.FindMany(ctx, golem.Where(Episodes.ID.Eq(input.ID)), golem.Select[Episode](Episodes.ShowID), golem.Take[Episode](1))
	if err != nil {
		return err
	}
	oldRoot := input.ShowID
	if len(rows) == 1 {
		var present bool
		oldRoot, present = golem.Value(rows[0], Episodes.ShowID).Get()
		if !present {
			return fmt.Errorf("ingest source identity is unavailable")
		}
	}
	roots := []golem.UUID{input.ShowID}
	if oldRoot != input.ShowID {
		roots = append(roots, oldRoot)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].String() < roots[j].String() })
	keys := make([]golem.LockKey, len(roots))
	for index, root := range roots {
		keys[index] = maintenanceLock(root)
	}
	if err := lock(ctx, keys...); err != nil {
		return err
	}
	for _, root := range roots {
		if _, err := fenceShow(ctx, shows, root, false, false); err != nil {
			return err
		}
	}
	if len(rows) == 0 {
		_, err := episodes.Create(ctx, Episodes.Create(Episodes.ID.Create(input.ID), Episodes.ShowID.Create(input.ShowID), Episodes.Title.Create(input.Title)))
		return err
	}
	count, err := episodes.UpdateMany(ctx, Episodes.ID.Eq(input.ID).And(Episodes.ShowID.Eq(oldRoot)), Episodes.UpdateMany(Episodes.ShowID.Set(input.ShowID), Episodes.Title.Set(input.Title)))
	if err == nil && count != 1 {
		return ErrFenceChanged
	}
	return err
}

// ProcessPage is the actual registered queue handler's work. It owns one
// bounded transaction, not the queue acknowledgement; replay after commit is
// fenced by the persisted generation/sequence before any destructive work.
func (service *Maintenance) ProcessPage(ctx context.Context, page CleanupPage) error {
	if service == nil || service.Application == nil || page.TaskID == (golem.UUID{}) || page.Generation == (golem.UUID{}) || page.Sequence < 0 {
		return queue.Terminal(fmt.Errorf("maintenance page identity is invalid"))
	}
	return service.Application.System().Transaction(ctx, func(tx *SystemTx[Principal]) error {
		return service.ProcessPageIntoTx(ctx, tx, page)
	}, golem.RetryConflicts(3))
}

// ProcessPageIntoTx shares the handler body for application composition. The
// queue acknowledgement remains outside this transaction in the queue worker.
func (service *Maintenance) ProcessPageIntoTx(ctx context.Context, tx *SystemTx[Principal], page CleanupPage) error {
	if service == nil || tx == nil || page.TaskID == (golem.UUID{}) || page.Generation == (golem.UUID{}) || page.Sequence < 0 {
		return queue.Terminal(fmt.Errorf("maintenance page identity is invalid"))
	}
	rows, err := tx.RemovalTasks.FindMany(ctx, golem.Where(RemovalTasks.ID.Eq(page.TaskID)), golem.Take[RemovalTask](1))
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return queue.Terminal(fmt.Errorf("maintenance task is absent"))
	}
	row := rows[0]
	generation, gOK := golem.Value(row, RemovalTasks.Generation).Get()
	sequence, sOK := golem.Value(row, RemovalTasks.Sequence).Get()
	completed, cOK := golem.Value(row, RemovalTasks.Completed).Get()
	showID, rOK := golem.Value(row, RemovalTasks.ShowID).Get()
	phase, pOK := golem.Value(row, RemovalTasks.Phase).Get()
	deleted, dOK := golem.Value(row, RemovalTasks.Deleted).Get()
	if !gOK || !sOK || !cOK || !rOK || !pOK || !dOK {
		return fmt.Errorf("maintenance progress is incomplete")
	}
	if generation != page.Generation || completed || sequence > page.Sequence {
		return nil
	}
	if sequence != page.Sequence || sequence == math.MaxInt32 {
		return queue.Terminal(fmt.Errorf("maintenance page is ahead of durable progress or exhausted"))
	}
	if err := tx.LockKeys(ctx, maintenanceLock(showID)); err != nil {
		return err
	}
	if _, err := fenceShow(ctx, tx.Shows, showID, true, true); err != nil {
		return err
	}
	var touched int64
	switch phase {
	case 0:
		leaves, err := tx.Assets.FindMany(ctx, golem.Where(Assets.Episode.Is(Episodes.ShowID.Eq(showID))), golem.Select[Asset](Assets.ID), golem.OrderBy(Assets.ID.Asc()), golem.Take[Asset](CleanupLeafPageSize))
		if err != nil {
			return err
		}
		ids := make([]golem.UUID, len(leaves))
		for index, leaf := range leaves {
			var present bool
			ids[index], present = golem.Value(leaf, Assets.ID).Get()
			if !present {
				return fmt.Errorf("maintenance leaf identity is unavailable")
			}
		}
		if len(ids) == 0 {
			phase = 1
		} else {
			touched, err = tx.Assets.DeleteMany(ctx, Assets.ID.In(ids...))
			if err != nil {
				return err
			}
			if touched != int64(len(ids)) {
				return ErrFenceChanged
			}
		}
	case 1:
		// Prove no private descendants remain before selecting parent pages;
		// the fixed page bound must bound physical effects, not only parents.
		remaining, err := tx.Assets.Count(ctx, golem.Where(Assets.Episode.Is(Episodes.ShowID.Eq(showID))))
		if err != nil {
			return err
		}
		if remaining != 0 {
			return fmt.Errorf("maintenance leaf phase is incomplete")
		}
		episodes, err := tx.Episodes.FindMany(ctx, golem.Where(Episodes.ShowID.Eq(showID)), golem.Select[Episode](Episodes.ID), golem.OrderBy(Episodes.ID.Asc()), golem.Take[Episode](CleanupLeafPageSize))
		if err != nil {
			return err
		}
		ids := make([]golem.UUID, len(episodes))
		for index, episode := range episodes {
			var present bool
			ids[index], present = golem.Value(episode, Episodes.ID).Get()
			if !present {
				return fmt.Errorf("maintenance episode identity is unavailable")
			}
		}
		if len(ids) == 0 {
			phase = 2
		} else {
			touched, err = tx.Episodes.DeleteMany(ctx, Episodes.ID.In(ids...))
			if err != nil {
				return err
			}
			if touched != int64(len(ids)) {
				return ErrFenceChanged
			}
		}
	case 2:
		remaining, err := tx.Episodes.Count(ctx, golem.Where(Episodes.ShowID.Eq(showID)))
		if err != nil {
			return err
		}
		if remaining != 0 {
			return fmt.Errorf("maintenance episode phase is incomplete")
		}
		if _, err := tx.Shows.Delete(ctx, Shows.ByID.Value(showID)); err != nil {
			return err
		}
		completed = true
	default:
		return queue.Terminal(fmt.Errorf("maintenance phase is unknown"))
	}
	if touched > CleanupLeafPageSize || int64(deleted)+touched > math.MaxInt32 {
		return fmt.Errorf("maintenance progress bound exceeded")
	}
	count, err := tx.RemovalTasks.UpdateMany(ctx, RemovalTasks.ID.Eq(page.TaskID).And(RemovalTasks.Generation.Eq(page.Generation)).And(RemovalTasks.Sequence.Eq(page.Sequence)).And(RemovalTasks.Completed.Eq(false)), RemovalTasks.UpdateMany(RemovalTasks.Sequence.Set(sequence+1), RemovalTasks.Phase.Set(phase), RemovalTasks.Deleted.Set(deleted+int32(touched)), RemovalTasks.Completed.Set(completed)))
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenceChanged
	}
	if completed {
		return nil
	}
	next, err := service.pending(CleanupPage{TaskID: page.TaskID, Generation: generation, Sequence: sequence + 1})
	if err != nil {
		return err
	}
	_, err = tx.Enqueue(ctx, next)
	return err
}
