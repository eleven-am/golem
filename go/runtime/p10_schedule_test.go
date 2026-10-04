package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

type p10ScheduleKey struct{}

type p10ScheduleEnd int

const (
	p10ScheduleWaits p10ScheduleEnd = iota
	p10ScheduleReturnsMidFlight
	p10ScheduleFailsMidFlight
	p10SchedulePanicsMidFlight
)

type p10ScheduleSource int

const (
	p10SourceHook p10ScheduleSource = iota
	p10SourceFresh
	p10SourceRetained
	p10SourceOtherHook
	p10SourceCancelled
	p10SourceDetached
	p10SourceCount
)

type p10ScheduleAction int

const (
	p10ActionExecutorWrite p10ScheduleAction = iota
	p10ActionExecutorRead
	p10ActionNestedDirectWrite
	p10ActionNestedRead
	p10ActionEscapedExecutorWrite
	p10ActionDetachedDirectWrite
	p10ActionOuterExecutorWrite
	p10ActionSpawnedDirectWrite
	p10ActionHookedRead
	p10ActionCount
)

type p10ScheduleReadKey struct{}

type p10ScheduleRead struct {
	child *p10ScheduleWrite
	ran   atomic.Bool
}

var errP10ReadNeverRanItsHook = errors.New("the read never reached its hook")

type p10ScheduleStep struct {
	action p10ScheduleAction
	source p10ScheduleSource
	child  *p10ScheduleWrite
}

type p10ScheduleWrite struct {
	id       golem.UUID
	parent   *p10ScheduleWrite
	root     *p10ScheduleWrite
	hookCtx  context.Context
	contexts []context.Context
	depth    int
	steps    []p10ScheduleStep
	fails    bool
	mu       sync.Mutex
	done     bool
	err      error
	outer    golem.HookExecutor
}

func (schedule *p10Schedule) finish(write *p10ScheduleWrite, err error) {
	write.mu.Lock()
	write.done, write.err = true, err
	write.mu.Unlock()
	if write.parent != nil {
		return
	}
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	schedule.retained = append(schedule.retained, write.contexts...)
	write.contexts = nil
}

func (write *p10ScheduleWrite) persisted(committed bool) bool {
	if !committed {
		return false
	}
	for current := write; current != nil; current = current.parent {
		current.mu.Lock()
		ok := current.done && current.err == nil
		current.mu.Unlock()
		if !ok {
			return false
		}
	}
	return true
}

type p10Schedule struct {
	t          *testing.T
	fixture    *p10OperationFixture
	tx         *p10operations.CallerTx[p10operations.Principal]
	mu         sync.Mutex
	random     *rand.Rand
	next       int
	base       int
	writes     []*p10ScheduleWrite
	retained   []context.Context
	pending    sync.WaitGroup
	progress   atomic.Int64
	unexpected atomic.Pointer[error]
	readHooks  atomic.Int64
}

func (schedule *p10Schedule) intn(n int) int {
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	return schedule.random.Intn(n)
}

func (schedule *p10Schedule) newWrite(parent *p10ScheduleWrite) *p10ScheduleWrite {
	schedule.mu.Lock()
	schedule.next++
	write := &p10ScheduleWrite{id: p10OperationID(schedule.t, schedule.base+schedule.next), parent: parent}
	write.root = write
	if parent != nil {
		write.depth = parent.depth + 1
		write.root = parent.root
	}
	schedule.writes = append(schedule.writes, write)
	schedule.mu.Unlock()
	write.fails = write.depth > 0 && schedule.intn(4) == 0
	if write.depth >= 2 {
		return write
	}
	for count := schedule.intn(4); count > 0; count-- {
		step := p10ScheduleStep{action: p10ScheduleAction(schedule.intn(int(p10ActionCount))), source: p10ScheduleSource(schedule.intn(int(p10SourceCount)))}
		if step.action == p10ActionOuterExecutorWrite && parent == nil {
			step.action = p10ActionExecutorWrite
		}
		if step.action == p10ActionSpawnedDirectWrite && parent != nil {
			step.action = p10ActionNestedDirectWrite
		}
		switch step.action {
		case p10ActionExecutorWrite, p10ActionNestedDirectWrite, p10ActionEscapedExecutorWrite, p10ActionOuterExecutorWrite, p10ActionHookedRead:
			step.child = schedule.newWrite(write)
		case p10ActionDetachedDirectWrite, p10ActionSpawnedDirectWrite:
			step.child = schedule.newWrite(nil)
		}
		write.steps = append(write.steps, step)
	}
	return write
}

func (schedule *p10Schedule) retain(write *p10ScheduleWrite, ctx context.Context) {
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	write.hookCtx = ctx
	write.root.contexts = append(write.root.contexts, ctx)
}

func (schedule *p10Schedule) retainedContext() context.Context {
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	if len(schedule.retained) == 0 {
		return context.Background()
	}
	return schedule.retained[schedule.random.Intn(len(schedule.retained))]
}

func (schedule *p10Schedule) context(source p10ScheduleSource, write *p10ScheduleWrite, hook context.Context) context.Context {
	switch source {
	case p10SourceFresh:
		return context.Background()
	case p10SourceOtherHook:
		if write.parent != nil {
			schedule.mu.Lock()
			defer schedule.mu.Unlock()
			return write.parent.hookCtx
		}
		return schedule.retainedContext()
	case p10SourceRetained:
		return schedule.retainedContext()
	case p10SourceCancelled:
		cancelled, cancel := context.WithCancel(hook)
		cancel()
		return cancelled
	case p10SourceDetached:
		return context.WithoutCancel(hook)
	}
	return hook
}

func (schedule *p10Schedule) hook(ctx context.Context, executor golem.HookExecutor) error {
	write, _ := ctx.Value(p10ScheduleKey{}).(*p10ScheduleWrite)
	if write == nil {
		return nil
	}
	schedule.retain(write, ctx)
	for _, step := range write.steps {
		switch step.action {
		case p10ActionExecutorWrite:
			step.child.outer = executor
			callCtx := context.WithValue(schedule.context(step.source, write, ctx), p10ScheduleKey{}, step.child)
			schedule.finish(step.child, schedule.fixture.createTeam(callCtx, executor, step.child.id))
		case p10ActionOuterExecutorWrite:
			step.child.outer = write.outer
			callCtx := context.WithValue(ctx, p10ScheduleKey{}, step.child)
			schedule.finish(step.child, schedule.fixture.createTeam(callCtx, write.outer, step.child.id))
		case p10ActionExecutorRead:
			_, _ = golem.HookFindManyRows(schedule.context(step.source, write, ctx), executor, p10operations.GolemGeneratedTeamDescriptor, golem.Where(p10operations.Teams.Owner.Eq("alpha")))
		case p10ActionNestedDirectWrite:
			step.child.outer = executor
			callCtx := context.WithValue(schedule.context(step.source, write, ctx), p10ScheduleKey{}, step.child)
			_, err := schedule.tx.Teams.Create(callCtx, p10TeamInput(step.child.id))
			schedule.finish(step.child, err)
		case p10ActionNestedRead:
			_, _ = schedule.tx.Teams.Count(schedule.context(step.source, write, ctx), golem.Where(p10operations.Teams.Owner.Eq("alpha")))
		case p10ActionHookedRead:
			schedule.read(schedule.context(step.source, write, ctx), step.child)
		case p10ActionEscapedExecutorWrite:
			child := step.child
			child.outer = executor
			schedule.pending.Add(1)
			go func() {
				defer schedule.pending.Done()
				schedule.finish(child, schedule.fixture.createTeam(context.WithValue(context.Background(), p10ScheduleKey{}, child), executor, child.id))
			}()
		case p10ActionSpawnedDirectWrite:
			child := step.child
			schedule.pending.Add(1)
			go func() {
				defer schedule.pending.Done()
				_, err := schedule.tx.Teams.Create(context.WithValue(ctx, p10ScheduleKey{}, child), p10TeamInput(child.id))
				schedule.finish(child, err)
			}()
		case p10ActionDetachedDirectWrite:
			child := step.child
			schedule.pending.Add(1)
			go func() {
				defer schedule.pending.Done()
				_, err := schedule.tx.Teams.Create(context.WithValue(context.Background(), p10ScheduleKey{}, child), p10TeamInput(child.id))
				schedule.finish(child, err)
			}()
		}
	}
	if write.fails {
		return errP10FailingInnerHook
	}
	return nil
}

func (schedule *p10Schedule) read(ctx context.Context, child *p10ScheduleWrite) error {
	read := &p10ScheduleRead{child: child}
	_, err := schedule.tx.Teams.FindMany(context.WithValue(ctx, p10ScheduleReadKey{}, read), golem.Where(p10operations.Teams.Owner.Eq("alpha")))
	if child != nil && !read.ran.Load() {
		schedule.finish(child, errP10ReadNeverRanItsHook)
	}
	return err
}

func (schedule *p10Schedule) readHook(ctx context.Context) error {
	read, _ := ctx.Value(p10ScheduleReadKey{}).(*p10ScheduleRead)
	if read == nil || !read.ran.CompareAndSwap(false, true) {
		return nil
	}
	schedule.readHooks.Add(1)
	if _, err := schedule.tx.Teams.Count(ctx, golem.Where(p10operations.Teams.Owner.Eq("alpha"))); err != nil {
		failure := fmt.Errorf("a read hook's own transaction call failed: %w", err)
		schedule.unexpected.Store(&failure)
	}
	if read.child == nil {
		return nil
	}
	_, err := schedule.tx.Teams.Create(context.WithValue(ctx, p10ScheduleKey{}, read.child), p10TeamInput(read.child.id))
	schedule.finish(read.child, err)
	if err != nil && !read.child.fails {
		failure := fmt.Errorf("a write made by a read hook through its own context failed: %w", err)
		schedule.unexpected.Store(&failure)
	}
	return nil
}

func (schedule *p10Schedule) root(worker int) {
	for round := schedule.intn(4) + 1; round > 0; round-- {
		var err error
		switch schedule.intn(5) {
		case 0, 1:
			write := schedule.newWrite(nil)
			_, err = schedule.tx.Teams.Create(context.WithValue(context.Background(), p10ScheduleKey{}, write), p10TeamInput(write.id))
			schedule.finish(write, err)
		case 2:
			write := schedule.newWrite(nil)
			_, err = p10operations.SystemEscape(schedule.tx).Teams.Create(context.Background(), p10TeamInput(write.id))
			schedule.finish(write, err)
		case 3:
			write := schedule.newWrite(nil)
			_, err = schedule.tx.Teams.Create(context.WithValue(schedule.retainedContext(), p10ScheduleKey{}, write), p10TeamInput(write.id))
			schedule.finish(write, err)
		case 4:
			var child *p10ScheduleWrite
			if schedule.intn(2) == 0 {
				child = schedule.newWrite(nil)
			}
			err = schedule.read(schedule.retainedContext(), child)
		}
		if err != nil && !p10ConcurrentUse(err) && !strings.Contains(err.Error(), "after its callback returned") && !strings.Contains(err.Error(), "execution binding is unavailable") {
			schedule.unexpected.Store(&err)
		}
		schedule.progress.Add(1)
	}
}

func p10ScheduleSeeds() []int64 {
	count := 40
	if value, err := strconv.Atoi(os.Getenv("GOLEM_P10_SCHEDULE_SEEDS")); err == nil && value > 0 {
		count = value
	}
	seeds := make([]int64, count)
	for index := range seeds {
		seeds[index] = int64(1000 + index*7919)
	}
	return seeds
}

func TestRandomTransactionSchedulesFinishAndStayConsistentAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		for index, seed := range p10ScheduleSeeds() {
			schedule := &p10Schedule{t: t, fixture: fixture, random: rand.New(rand.NewSource(seed)), base: 1000000 + index*10000}
			end := p10ScheduleEnd(schedule.intn(4))
			workers := schedule.intn(4) + 2
			midFlight := int64(schedule.intn(workers))
			p10operations.Reset(nil)
			p10operations.SetTeamHook(schedule.hook)
			p10operations.SetTeamReadHook(schedule.readHook)
			before := fixture.outboxRows(t)
			var transactionErr error
			var recovered any
			p10FinishesWithin(t, 60*time.Second, func() {
				func() {
					defer func() { recovered = recover() }()
					transactionErr = caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
						schedule.tx = tx
						var started sync.WaitGroup
						for worker := 0; worker < workers; worker++ {
							worker := worker
							schedule.pending.Add(1)
							started.Add(1)
							go func() {
								defer schedule.pending.Done()
								started.Done()
								schedule.root(worker)
							}()
						}
						started.Wait()
						if end != p10ScheduleWaits {
							p10AwaitCondition(func() bool { return schedule.progress.Load() >= midFlight })
						}
						switch end {
						case p10ScheduleReturnsMidFlight:
							return nil
						case p10ScheduleFailsMidFlight:
							return errP10CallbackStopped
						case p10SchedulePanicsMidFlight:
							panic("schedule panicked")
						}
						schedule.pending.Wait()
						return nil
					})
				}()
				schedule.pending.Wait()
			})
			if unexpected := schedule.unexpected.Load(); unexpected != nil {
				t.Fatalf("seed %d: a root call failed for a reason other than overlap or the callback ending: %v", seed, *unexpected)
			}
			if end == p10SchedulePanicsMidFlight {
				if recovered != "schedule panicked" {
					t.Fatalf("seed %d: recovered %v", seed, recovered)
				}
			} else if recovered != nil {
				t.Fatalf("seed %d: unexpected panic %v", seed, recovered)
			}
			if end == p10ScheduleFailsMidFlight && !errors.Is(transactionErr, errP10CallbackStopped) {
				t.Fatalf("seed %d: failed callback = %v", seed, transactionErr)
			}
			committed := recovered == nil && transactionErr == nil
			expected := 0
			for _, write := range schedule.writes {
				want := write.persisted(committed)
				if want {
					expected++
				}
				if _, ok := fixture.teamOwner(t, write.id); ok != want {
					t.Fatalf("seed %d end %d: team %s persisted=%t want %t (err=%v, transaction=%v)", seed, end, write.id, ok, want, write.err, transactionErr)
				}
			}
			if got := fixture.outboxRows(t) - before; got != expected {
				t.Fatalf("seed %d: outbox rows=%d want %d", seed, got, expected)
			}
			t.Log(fmt.Sprintf("seed %d end %d workers %d writes %d persisted %d read hooks %d transaction %v", seed, end, workers, len(schedule.writes), expected, schedule.readHooks.Load(), transactionErr))
		}
	})
}
