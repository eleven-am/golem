// Package runtime is the public application-runtime boundary used by generated
// Golem application packages. It owns immutable schema/provider state and
// creates a fresh actor policy set for every caller execution.
package runtime

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
	"github.com/eleven-am/golem/go/internal/subscription"
	"github.com/eleven-am/golem/go/observe"
	providerapi "github.com/eleven-am/golem/go/provider"
	"github.com/jmoiron/sqlx"
)

// Config contains application infrastructure and generated artifacts. P is the
// application's authenticated principal type; A is the actor type consumed by
// model policy methods.
type Config[P, A any] struct {
	Database               *providerapi.Database
	Embeddings             embedding.Registry
	Bundle                 golem.SchemaBundle
	Bindings               golem.ApplicationBindings[A]
	Descriptors            golem.ApplicationDescriptors
	ReadLimits             ReadLimits
	MutationLimits         MutationLimits
	AnalyticsLimits        AnalyticsLimits
	EventRegistry          golem.EventRegistry
	EventFactories         EventFactoryRegistry
	EventLimits            events.Limits
	EventTransport         events.EventTransport
	Observer               observe.Observer
	CDCAdapters            []events.CDCAdapter
	ReportEventOperator    events.OperatorAudit
	HistoricalEventBundles []golem.SchemaBundle
	AfterCommitError       func(context.Context, golem.AfterCommitFailure)
	AuditPrincipal         func(P) string
	ReportScopedQuery      func(context.Context, golem.ScopedAuditRecord)
	ResolvePrincipal       func(context.Context, P) (A, error)
	SnapshotPrincipal      func(P) (P, error)
	// SnapshotActor transfers ownership of mutable actor shapes into one stable
	// caller snapshot shared by policy construction and hooks. When omitted,
	// ForPrincipal accepts only deeply immutable value actors.
	SnapshotActor func(A) (A, error)
}

// App contains immutable process-wide metadata only. No actor, policy set,
// loader, request context, or decoded row is retained here.
type App[P, A any] struct {
	databaseHandle    *providerapi.Database
	database          *sqlx.DB
	provider          policyir.Provider
	registry          *schema.Registry
	providers         policyir.ProviderSet
	capabilities      policysql.CapabilityProof
	bindings          golem.ApplicationBindings[A]
	descriptors       golem.ApplicationDescriptors
	resolvePrincipal  func(context.Context, P) (A, error)
	snapshotActor     func(A) (A, error)
	readLimits        normalizedReadLimits
	mutationLimits    normalizedMutationLimits
	analyticsLimits   normalizedAnalyticsLimits
	eventRegistry     golem.EventRegistry
	eventFactories    EventFactoryRegistry
	eventLimits       events.Limits
	eventTransport    events.EventTransport
	eventObserver     events.Observer
	observer          observe.Observer
	eventSchemas      *eventSchemaHistory
	eventProvider     golem.Provider
	eventPublisher    eventPublisherRunner
	eventOperator     events.Operator
	eventAdapters     []events.CDCAdapter
	eventCDCWorkers   []eventCDCWorker
	eventAdapterNames []string
	eventModels       []golem.ModelID
	eventTransportABI events.TransportCapabilities
	eventRunning      atomic.Bool
	snapshotPrincipal func(P) (P, error)
	eventMu           sync.Mutex
	eventHubs         map[golem.ModelID]*subscription.ModelHub[any]
	nextSubscription  atomic.Uint64
	afterCommitError  func(context.Context, golem.AfterCommitFailure)
	auditPrincipal    func(P) string
	reportScopedQuery func(context.Context, golem.ScopedAuditRecord)
	nextExecution     atomic.Uint64
	semantic          *semanticruntime.Manager
}

// Caller is one principal-bound execution. Its policy set and identity are not
// shared with another Caller, even when the principals compare equal.
type Caller[P, A any] struct {
	app       *App[P, A]
	policies  *policyruntime.Set
	actor     A
	execution uint64
	executor  *executionBinding
	auditID   string
	principal P
}

// System is an explicit unrestricted capability. It never resolves a
// principal, builds caller policy, or invokes caller hooks.
type System[P, A any] struct {
	app      *App[P, A]
	executor *executionBinding
}

// PreparedRead is an opaque, schema-bound request. Later P3 planning and
// execution stages consume the private IR without reopening public identities.
type PreparedRead struct {
	request  readir.Request
	policies *policyruntime.Set
	system   bool
	executor *executionBinding
}

func (prepared PreparedRead) ModelID() golem.ModelID {
	return golem.ModelID(prepared.request.ModelID())
}
func (prepared PreparedRead) Operation() golem.ReadOperation {
	return externalOperation(prepared.request.Operation())
}
func (prepared PreparedRead) IsSystem() bool { return prepared.system }

// Open validates the complete generated artifact graph, fingerprints, provider
// selection, and live provider capabilities before publishing an App.
func Open[P, A any](ctx context.Context, config Config[P, A]) (result *App[P, A], resultErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("P3_RUNTIME_CONFIG: context and database are required")
	}
	databaseHandle := config.Database
	if databaseHandle == nil {
		return nil, fmt.Errorf("P3_RUNTIME_CONFIG: context and database are required")
	}
	database := databaseHandle.UnsafeSQLX()
	if database == nil {
		return nil, fmt.Errorf("P3_RUNTIME_CONFIG: database handle is zero or closed")
	}
	pool := databaseHandle.Pool()
	if pool.MaximumOpen() < 1 || pool.MaximumIdle() < 1 || pool.MaximumIdle() > pool.MaximumOpen() {
		return nil, fmt.Errorf("P8_RUNTIME_DATABASE: verified database pool configuration is invalid")
	}
	// UnsafeSQLX callers may have changed these knobs since provider.Open.
	// Restore the sealed profile before the runtime capability reproof.
	database.SetMaxOpenConns(pool.MaximumOpen())
	database.SetMaxIdleConns(pool.MaximumIdle())
	database.SetConnMaxLifetime(pool.ConnectionMaximumLifetime())
	database.SetConnMaxIdleTime(pool.ConnectionMaximumIdleTime())
	if database.Stats().MaxOpenConnections != pool.MaximumOpen() {
		return nil, fmt.Errorf("P8_RUNTIME_DATABASE: database pool profile could not be restored")
	}
	if database.Stats().InUse != 0 {
		return nil, fmt.Errorf("P8_RUNTIME_DATABASE: database pool must be idle during application startup")
	}
	providerIdentity := databaseHandle.Provider()
	ctx, openObservation := observeexec.Begin(ctx, config.Observer, providerIdentity, golem.ModelID{}, observe.KindRuntime, observe.OperationRuntimeOpen, observe.PhaseOpen)
	openReason := observe.ReasonInvalidInput
	defer func() {
		if resultErr == nil {
			observeexec.Finish(openObservation, observe.OutcomeSuccess, observe.ReasonNone)
			return
		}
		observeexec.Finish(openObservation, observe.OutcomeRefused, openReason)
	}()
	if databaseHandle.Capabilities().Provider() != providerIdentity {
		openReason = observe.ReasonCapability
		return nil, fmt.Errorf("P3_RUNTIME_PROVIDER: database handle capabilities do not match its provider")
	}
	provider, ok := internalProvider(providerIdentity)
	if !ok {
		openReason = observe.ReasonProvider
		return nil, fmt.Errorf("P3_RUNTIME_PROVIDER: unsupported database provider %q", providerIdentity)
	}
	if config.ResolvePrincipal == nil {
		return nil, fmt.Errorf("P3_RUNTIME_CONFIG: principal resolver is required")
	}
	readLimits, err := normalizeReadLimits(config.ReadLimits)
	if err != nil {
		return nil, fmt.Errorf("P3_RUNTIME_CONFIG: invalid read limits: %w", err)
	}
	mutationLimits, err := normalizeMutationLimits(config.MutationLimits)
	if err != nil {
		return nil, fmt.Errorf("P4_RUNTIME_CONFIG: invalid mutation limits: %w", err)
	}
	analyticsLimits, err := normalizeAnalyticsLimits(config.AnalyticsLimits)
	if err != nil {
		return nil, fmt.Errorf("P6_RUNTIME_CONFIG: invalid analytics limits: %w", err)
	}
	registry, err := schema.New(config.Bundle)
	if err != nil {
		return nil, fmt.Errorf("P3_RUNTIME_SCHEMA: %w", err)
	}
	if registry.HasScopedReads() && (config.AuditPrincipal == nil || config.ReportScopedQuery == nil) {
		return nil, fmt.Errorf("P6_RUNTIME_CONFIG: AuditPrincipal and ReportScopedQuery are required when scoped reads are enabled")
	}
	eventSchemas, err := newEventSchemaHistory(registry, config.HistoricalEventBundles)
	if err != nil {
		return nil, err
	}
	eventLimits, err := validateEventConfiguration(config, registry, providerIdentity)
	if err != nil {
		return nil, err
	}
	if config.Bindings.GenerationDigest() != registry.GenerationDigest() || config.Descriptors.GenerationDigest() != registry.GenerationDigest() {
		return nil, fmt.Errorf("P3_RUNTIME_GENERATION: bindings, descriptors, and schema differ")
	}
	providers, err := providerSet(registry)
	if err != nil || !providers.Contains(provider) {
		return nil, fmt.Errorf("P3_RUNTIME_PROVIDER: provider is not declared by the active schema")
	}
	if err := validateInventory(config.Bindings, config.Descriptors, registry); err != nil {
		return nil, err
	}
	if err := validateAfterCommitHandler(config.Bindings, config.AfterCommitError); err != nil {
		return nil, err
	}
	expected, err := selectedPhysicalSchema(config.Bundle, providerIdentity)
	if err != nil {
		return nil, fmt.Errorf("P3_RUNTIME_SCHEMA: generated physical schema is invalid")
	}
	semanticInventory, err := semanticruntime.NewInventory(expected, config.Embeddings)
	if err != nil {
		openReason = observe.ReasonCapability
		return nil, err
	}
	semanticManager, err := semanticruntime.NewManager(database, expected.Provider.Provider, expected, semanticInventory, config.Observer)
	if err != nil {
		openReason = observe.ReasonCapability
		return nil, err
	}
	migrationStartup, err := prepareReviewedMigrationStartup(databaseHandle, config.Bundle, providerIdentity, expected)
	if err != nil {
		openReason = observe.ReasonMigrationHistory
		return nil, fmt.Errorf("P8_RUNTIME_MIGRATION: reviewed migration state is invalid")
	}
	migrationContext, migrationObservation := observeexec.BeginChild(ctx, golem.ModelID{}, observe.KindMigration, observe.OperationMigrationInspect, observe.PhaseVerify)
	if err := verifyReviewedMigrationLedger(migrationContext, database, providerIdentity, expected.System, migrationStartup); err != nil {
		observeexec.Finish(migrationObservation, observe.OutcomeRefused, observe.ReasonMigrationHistory)
		openReason = observe.ReasonMigrationHistory
		return nil, fmt.Errorf("P8_RUNTIME_MIGRATION: live migration state is incompatible")
	}
	observeexec.Finish(migrationObservation, observe.OutcomeSuccess, observe.ReasonNone)
	openReason = observe.ReasonCapability
	proof, err := proveCapabilities(ctx, database, provider, [32]byte(registry.ModelFingerprint()), pool.MaximumOpen())
	if err != nil {
		return nil, fmt.Errorf("P3_RUNTIME_CAPABILITY: database capabilities are incompatible")
	}
	if err := verifyPhysical(ctx, database, provider, expected); err != nil {
		openReason = observe.ReasonSchemaDrift
		return nil, fmt.Errorf("P3_RUNTIME_DRIFT: managed database schema is incompatible")
	}
	app := &App[P, A]{databaseHandle: databaseHandle, database: database, provider: provider, registry: registry, providers: providers, capabilities: proof, bindings: config.Bindings, descriptors: config.Descriptors, resolvePrincipal: config.ResolvePrincipal, snapshotActor: config.SnapshotActor, readLimits: readLimits, mutationLimits: mutationLimits, analyticsLimits: analyticsLimits, eventRegistry: config.EventRegistry, eventFactories: config.EventFactories, eventLimits: eventLimits, eventTransport: config.EventTransport, observer: config.Observer, eventSchemas: eventSchemas, eventProvider: providerIdentity, snapshotPrincipal: config.SnapshotPrincipal, eventHubs: make(map[golem.ModelID]*subscription.ModelHub[any]), afterCommitError: config.AfterCommitError, auditPrincipal: config.AuditPrincipal, reportScopedQuery: config.ReportScopedQuery, semantic: semanticManager}
	app.eventObserver = adaptEventObserver(config.Observer, providerIdentity)
	if err := app.initializeEventRuntime(config.CDCAdapters, config.ReportEventOperator); err != nil {
		return nil, err
	}
	return app, nil
}

// RefreshSemanticIndexes reconciles every declared semantic index with its
// current source rows. Unchanged rows do not call the embedding provider.
func (app *App[P, A]) RefreshSemanticIndexes(ctx context.Context) error {
	if app == nil || app.semantic == nil || ctx == nil {
		return fmt.Errorf("P9_SEMANTIC_RUNTIME: application and context are required")
	}
	return app.semantic.RefreshAll(ctx)
}

func validateEventConfiguration[P, A any](config Config[P, A], registry *schema.Registry, providerIdentity golem.Provider) (events.Limits, error) {
	limits, err := events.NormalizeLimits(config.EventLimits)
	if err != nil {
		return events.Limits{}, err
	}
	subscribed := make(map[golem.ModelID]bool)
	for _, descriptor := range config.Descriptors.Models() {
		model, ok := registry.Model(descriptor.ModelID())
		if ok && model.SubscriptionsEnabled() {
			subscribed[descriptor.ModelID()] = true
		}
	}
	generated := config.EventRegistry.Models()
	if len(subscribed) == 0 {
		if len(generated) != 0 {
			return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: event registry exists without subscription-enabled models")
		}
		if len(config.CDCAdapters) != 0 {
			return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: CDC adapters require subscription-enabled models")
		}
		return limits, nil
	}
	if config.EventRegistry.GenerationDigest() != registry.GenerationDigest() || len(generated) != len(subscribed) {
		return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: event registry and active schema differ")
	}
	if config.EventFactories.GenerationDigest() != registry.GenerationDigest() || len(config.EventFactories.modelIDs()) != len(subscribed) {
		return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: event factories and active schema differ")
	}
	for _, metadata := range generated {
		model, ok := registry.Model(metadata.ModelID())
		fingerprint, _, eventOK := model.EventSchema()
		digest := metadata.EventSchemaDigest()
		if !ok || !subscribed[metadata.ModelID()] || !eventOK || string(fingerprint) != hex.EncodeToString(digest[:]) {
			return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: generated event model and active schema differ")
		}
		factory := config.EventFactories.factories[metadata.ModelID()]
		if factory == nil || factory.EventSchemaDigest() != metadata.EventSchemaDigest() {
			return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: generated event factory and schema differ")
		}
	}
	if config.EventTransport == nil {
		return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: event transport is required for subscription-enabled models")
	}
	if config.ReportEventOperator == nil {
		return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: ReportEventOperator is required for subscription-enabled models")
	}
	capabilities := events.CapabilitiesOf(config.EventTransport)
	if _, err := events.NewTransportCapabilities(capabilities.Identity(), capabilities.Scope(), capabilities.Durable()); err != nil {
		return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: event transport capabilities are invalid")
	}
	if capabilities.Scope() == events.TransportScopeCrossProcess {
		if _, ok := config.EventTransport.(events.RuntimeBindableTransport); !ok {
			return events.Limits{}, fmt.Errorf("GOLEM_EVENT_CONFIG: cross-process event transport requires runtime binding")
		}
	}
	if _, err := events.ValidateCDCAdapters(providerIdentity, config.CDCAdapters); err != nil {
		return events.Limits{}, err
	}
	return limits, nil
}

func validateAfterCommitHandler[A any](bindings golem.ApplicationBindings[A], handler func(context.Context, golem.AfterCommitFailure)) error {
	if handler != nil {
		return nil
	}
	for _, hook := range bindings.RuntimeHookInventory() {
		if hook.Phase == golem.HookAfterCommit {
			return fmt.Errorf("P4_RUNTIME_CONFIG: AfterCommitError is required when after-commit hooks are configured")
		}
	}
	return nil
}

// ForPrincipal resolves the principal and builds every model policy exactly
// once for this fresh execution. Resolution or policy failure is never treated
// as system access.
func (app *App[P, A]) ForPrincipal(ctx context.Context, principal P) (*Caller[P, A], error) {
	if app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "begin", golem.ModelID{}, golem.FieldID{}, "principal could not be resolved", fmt.Errorf("nil application or context"))
	}
	resolved, err := app.resolvePrincipal(ctx, principal)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "begin", golem.ModelID{}, golem.FieldID{}, "principal could not be resolved", err)
	}
	actor, err := snapshotActor(resolved, app.snapshotActor)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "begin", golem.ModelID{}, golem.FieldID{}, "principal actor could not be snapshotted", err)
	}
	policies, err := policyruntime.Build(policyruntime.BuildRequest[A]{Bindings: app.bindings, Actor: actor, Registry: app.registry, Provider: app.provider, Capabilities: app.capabilities})
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "begin", golem.ModelID{}, golem.FieldID{}, "principal policy could not be established", err)
	}
	execution := app.nextExecution.Add(1)
	if execution == 0 {
		return nil, fmt.Errorf("P3_RUNTIME_EXECUTION: execution identity exhausted")
	}
	auditID := ""
	if app.auditPrincipal != nil {
		auditID = app.auditPrincipal(principal)
	}
	return &Caller[P, A]{app: app, policies: policies, actor: actor, execution: execution, executor: databaseExecution(app.database), auditID: auditID, principal: principal}, nil
}

func (app *App[P, A]) System() System[P, A] {
	if app == nil {
		return System[P, A]{}
	}
	return System[P, A]{app: app, executor: databaseExecution(app.database)}
}

// Prepare binds a caller request to the exact schema fingerprint and execution
// policy set. It performs no SQL and exposes no internal request representation.
func (caller *Caller[P, A]) Prepare(request golem.FrozenReadRequest) (PreparedRead, error) {
	if caller == nil || caller.app == nil || caller.policies == nil || caller.execution == 0 {
		return PreparedRead{}, golem.RuntimeReadError(golem.CodeUnauthenticated, "read", request.ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	bound, err := readbind.Request(request, caller.app.registry, caller.app.providers)
	if err != nil {
		return PreparedRead{}, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(request.Operation()), request.ModelID(), golem.FieldID{}, "read request is invalid", err)
	}
	if _, ok := caller.policies.Policy(bound.ModelID()); !ok {
		return PreparedRead{}, golem.RuntimeReadError(golem.CodeForbidden, operationName(request.Operation()), request.ModelID(), golem.FieldID{}, "read is not permitted", fmt.Errorf("model policy is absent"))
	}
	if _, err := caller.executor.queryerFor(caller.app.database); err != nil {
		return PreparedRead{}, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(request.Operation()), request.ModelID(), golem.FieldID{}, "read execution binding is unavailable", err)
	}
	return PreparedRead{request: bound, policies: caller.policies, executor: caller.executor}, nil
}

func (system System[P, A]) Prepare(request golem.FrozenReadRequest) (PreparedRead, error) {
	if system.app == nil {
		return PreparedRead{}, fmt.Errorf("P3_RUNTIME_SYSTEM: system capability is unavailable")
	}
	bound, err := readbind.Request(request, system.app.registry, system.app.providers)
	if err != nil {
		return PreparedRead{}, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(request.Operation()), request.ModelID(), golem.FieldID{}, "read request is invalid", err)
	}
	if _, err := system.executor.queryerFor(system.app.database); err != nil {
		return PreparedRead{}, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(request.Operation()), request.ModelID(), golem.FieldID{}, "read execution binding is unavailable", err)
	}
	return PreparedRead{request: bound, system: true, executor: system.executor}, nil
}

// CallerExecuteFrozenRead is the model-erased P3 execution seam used by the
// generated GraphQL adapter. It preserves the ordinary caller lifecycle:
// generated typed before hooks may transform the frozen request, every
// transformation is rebound before later hooks observe it, P3 owns all policy
// and SQL work, and generated typed after hooks receive only masked rows.
func CallerExecuteFrozenRead[P, A any](ctx context.Context, caller *Caller[P, A], request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, operationName(request.Operation()), request.ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	envelope, err := golem.RuntimeReadHookRequestFromFrozen(request)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(request.Operation()), request.ModelID(), golem.FieldID{}, "read request is invalid", err)
	}
	hookContext := golem.RuntimeContextWithActor(ctx, caller.actor)
	transformed, err := golem.RuntimeInvokeReadBeforeHooks(hookContext, caller.app.bindings, envelope, func(value golem.RuntimeReadHookRequest) error {
		_, prepareErr := caller.Prepare(value.Request())
		return prepareErr
	})
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(request.Operation()), request.ModelID(), golem.FieldID{}, "read hook rejected the operation", err)
	}
	prepared, err := caller.Prepare(transformed.Request())
	if err != nil {
		return nil, err
	}
	rows, err := executeRuntimeRows(ctx, caller.app, prepared)
	if err != nil {
		return nil, err
	}
	found := len(rows) != 0
	switch prepared.Operation() {
	case golem.ReadFindUnique:
		if len(rows) == 0 {
			return nil, golem.RuntimeReadError(golem.CodeNotFound, "findUnique", prepared.ModelID(), golem.FieldID{}, "record not found", nil)
		}
		if len(rows) != 1 {
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "findUnique", prepared.ModelID(), golem.FieldID{}, "unique read returned an invalid cardinality", fmt.Errorf("rows=%d", len(rows)))
		}
	case golem.ReadFindFirst:
		if len(rows) > 1 {
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "findFirst", prepared.ModelID(), golem.FieldID{}, "first read returned an invalid cardinality", fmt.Errorf("rows=%d", len(rows)))
		}
	case golem.ReadFindMany:
	default:
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(prepared.Operation()), prepared.ModelID(), golem.FieldID{}, "operation is not row-shaped", nil)
	}
	if err := golem.RuntimeInvokeReadResultHooks(hookContext, caller.app.bindings, golem.RuntimeReadHookRows(transformed, rows, found)); err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(prepared.Operation()), prepared.ModelID(), golem.FieldID{}, "read hook rejected the result", err)
	}
	return rows, nil
}

func (caller *Caller[P, A]) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	return CallerExecuteFrozenRead(ctx, caller, request)
}

func executeRuntimeRows[P, A any](ctx context.Context, app *App[P, A], prepared PreparedRead) (result []golem.RuntimeModelRow, resultErr error) {
	if app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "read", prepared.ModelID(), golem.FieldID{}, "read execution is unavailable", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, observation := beginExecutionObservation(ctx, app, prepared.executor, prepared.ModelID(), observe.KindRead, readObservationOperation(prepared.Operation()))
	defer func() { finishObservation(observation, resultErr) }()
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return nil, publicPlanError(prepared, err)
	}
	executed, err := executePlan(ctx, app, prepared.executor, prepared.Operation(), planned)
	if err != nil {
		return nil, err
	}
	rows := make([]golem.RuntimeModelRow, len(executed))
	for index := range executed {
		rows[index] = executed[index].row
	}
	return rows, nil
}

// CallerFindMany is the generic execution primitive used by generated model
// clients. Application code normally calls caller.Posts.FindMany instead.
func CallerFindMany[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (resultRows []golem.Row[M], resultErr error) {
	if caller == nil || caller.app == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "findMany", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	ctx, observation := beginExecutionObservation(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), observe.KindRead, observe.OperationReadFindMany)
	defer func() { finishObservation(observation, resultErr) }()
	hookContext := golem.RuntimeContextWithActor(ctx, caller.actor)
	hookRequest := golem.RuntimeFindManyHookRequest(options)
	if err := invokeReadHookObserved(hookContext, caller.app, caller.executor, caller.app.bindings, descriptor.Metadata().ModelID(), golem.ReadFindMany, golem.HookFindMany, golem.HookBefore, hookRequest); err != nil {
		return nil, err
	}
	options = hookRequest.Options()
	frozen, err := golem.FreezeFindMany(descriptor, options...)
	if err != nil {
		return nil, err
	}
	prepared, err := caller.Prepare(frozen)
	if err != nil {
		return nil, err
	}
	rows, err := executeRows(ctx, caller.app, prepared, descriptor)
	if err != nil {
		return nil, err
	}
	result := golem.RuntimeFindManyHookResult(rows)
	if err := invokeReadHookObserved(hookContext, caller.app, caller.executor, caller.app.bindings, descriptor.Metadata().ModelID(), golem.ReadFindMany, golem.HookFindMany, golem.HookAfter, result); err != nil {
		return nil, err
	}
	return rows, nil
}

func SystemFindMany[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (resultRows []golem.Row[M], resultErr error) {
	ctx, observation := beginExecutionObservation(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), observe.KindRead, observe.OperationReadFindMany)
	defer func() { finishObservation(observation, resultErr) }()
	frozen, err := golem.FreezeFindMany(descriptor, options...)
	if err != nil {
		return nil, err
	}
	prepared, err := system.Prepare(frozen)
	if err != nil {
		return nil, err
	}
	return executeRows(ctx, system.app, prepared, descriptor)
}

func CallerFindFirst[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (resultRow golem.Row[M], resultFound bool, resultErr error) {
	if caller == nil || caller.app == nil {
		return golem.Row[M]{}, false, golem.RuntimeReadError(golem.CodeUnauthenticated, "findFirst", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	ctx, observation := beginExecutionObservation(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), observe.KindRead, observe.OperationReadFindFirst)
	defer func() { finishObservation(observation, resultErr) }()
	hookContext := golem.RuntimeContextWithActor(ctx, caller.actor)
	hookRequest := golem.RuntimeFindFirstHookRequest(options)
	if err := invokeReadHookObserved(hookContext, caller.app, caller.executor, caller.app.bindings, descriptor.Metadata().ModelID(), golem.ReadFindFirst, golem.HookFindFirst, golem.HookBefore, hookRequest); err != nil {
		return golem.Row[M]{}, false, err
	}
	options = hookRequest.Options()
	frozen, err := golem.FreezeFindFirst(descriptor, options...)
	if err != nil {
		return golem.Row[M]{}, false, err
	}
	prepared, err := caller.Prepare(frozen)
	if err != nil {
		return golem.Row[M]{}, false, err
	}
	rows, err := executeRows(ctx, caller.app, prepared, descriptor)
	if err != nil {
		return golem.Row[M]{}, false, err
	}
	found := len(rows) != 0
	var row golem.Row[M]
	if found {
		row = rows[0]
	}
	result := golem.RuntimeFindFirstHookResult(row, found)
	if err := invokeReadHookObserved(hookContext, caller.app, caller.executor, caller.app.bindings, descriptor.Metadata().ModelID(), golem.ReadFindFirst, golem.HookFindFirst, golem.HookAfter, result); err != nil {
		return golem.Row[M]{}, false, err
	}
	return row, found, nil
}

func SystemFindFirst[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (resultRow golem.Row[M], resultFound bool, resultErr error) {
	ctx, observation := beginExecutionObservation(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), observe.KindRead, observe.OperationReadFindFirst)
	defer func() { finishObservation(observation, resultErr) }()
	frozen, err := golem.FreezeFindFirst(descriptor, options...)
	if err != nil {
		return golem.Row[M]{}, false, err
	}
	prepared, err := system.Prepare(frozen)
	if err != nil {
		return golem.Row[M]{}, false, err
	}
	rows, err := executeRows(ctx, system.app, prepared, descriptor)
	if err != nil || len(rows) == 0 {
		return golem.Row[M]{}, false, err
	}
	return rows[0], true, nil
}

func CallerFindUnique[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], selector golem.UniqueSelectorValue[M], options ...golem.ReadOption[M]) (resultRow golem.Row[M], resultErr error) {
	if caller == nil || caller.app == nil {
		return golem.Row[M]{}, golem.RuntimeReadError(golem.CodeUnauthenticated, "findUnique", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	ctx, observation := beginExecutionObservation(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), observe.KindRead, observe.OperationReadFindUnique)
	defer func() { finishObservation(observation, resultErr) }()
	hookContext := golem.RuntimeContextWithActor(ctx, caller.actor)
	hookRequest := golem.RuntimeFindOneHookRequest(selector, options)
	if err := invokeReadHookObserved(hookContext, caller.app, caller.executor, caller.app.bindings, descriptor.Metadata().ModelID(), golem.ReadFindUnique, golem.HookFindOne, golem.HookBefore, hookRequest); err != nil {
		return golem.Row[M]{}, err
	}
	selector, options = hookRequest.Selector(), hookRequest.Options()
	frozen, err := golem.FreezeFindUnique(descriptor, selector, options...)
	if err != nil {
		return golem.Row[M]{}, err
	}
	prepared, err := caller.Prepare(frozen)
	if err != nil {
		return golem.Row[M]{}, err
	}
	rows, err := executeRows(ctx, caller.app, prepared, descriptor)
	if err != nil {
		return golem.Row[M]{}, err
	}
	if len(rows) == 0 {
		return golem.Row[M]{}, golem.RuntimeReadError(golem.CodeNotFound, "findUnique", frozen.ModelID(), golem.FieldID{}, "record not found", nil)
	}
	if len(rows) != 1 {
		return golem.Row[M]{}, golem.RuntimeReadError(golem.CodeBadUserInput, "findUnique", frozen.ModelID(), golem.FieldID{}, "unique read returned an invalid cardinality", fmt.Errorf("rows=%d", len(rows)))
	}
	row := rows[0]
	result := golem.RuntimeFindOneHookResult(row)
	if err := invokeReadHookObserved(hookContext, caller.app, caller.executor, caller.app.bindings, descriptor.Metadata().ModelID(), golem.ReadFindUnique, golem.HookFindOne, golem.HookAfter, result); err != nil {
		return golem.Row[M]{}, err
	}
	return row, nil
}

func SystemFindUnique[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], selector golem.UniqueSelectorValue[M], options ...golem.ReadOption[M]) (resultRow golem.Row[M], resultErr error) {
	ctx, observation := beginExecutionObservation(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), observe.KindRead, observe.OperationReadFindUnique)
	defer func() { finishObservation(observation, resultErr) }()
	frozen, err := golem.FreezeFindUnique(descriptor, selector, options...)
	if err != nil {
		return golem.Row[M]{}, err
	}
	prepared, err := system.Prepare(frozen)
	if err != nil {
		return golem.Row[M]{}, err
	}
	rows, err := executeRows(ctx, system.app, prepared, descriptor)
	if err != nil {
		return golem.Row[M]{}, err
	}
	if len(rows) == 0 {
		return golem.Row[M]{}, golem.RuntimeReadError(golem.CodeNotFound, "findUnique", frozen.ModelID(), golem.FieldID{}, "record not found", nil)
	}
	if len(rows) != 1 {
		return golem.Row[M]{}, golem.RuntimeReadError(golem.CodeBadUserInput, "findUnique", frozen.ModelID(), golem.FieldID{}, "unique read returned an invalid cardinality", fmt.Errorf("rows=%d", len(rows)))
	}
	return rows[0], nil
}

func CallerCount[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (result int64, resultErr error) {
	if caller == nil || caller.app == nil {
		return 0, golem.RuntimeReadError(golem.CodeUnauthenticated, "count", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	ctx, observation := beginExecutionObservation(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), observe.KindRead, observe.OperationReadCount)
	defer func() { finishObservation(observation, resultErr) }()
	frozen, err := golem.FreezeCount(descriptor, options...)
	if err != nil {
		return 0, err
	}
	prepared, err := caller.Prepare(frozen)
	if err != nil {
		return 0, err
	}
	return executeCount(ctx, caller.app, prepared)
}

func SystemCount[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (result int64, resultErr error) {
	ctx, observation := beginExecutionObservation(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), observe.KindRead, observe.OperationReadCount)
	defer func() { finishObservation(observation, resultErr) }()
	frozen, err := golem.FreezeCount(descriptor, options...)
	if err != nil {
		return 0, err
	}
	prepared, err := system.Prepare(frozen)
	if err != nil {
		return 0, err
	}
	return executeCount(ctx, system.app, prepared)
}

func invokeReadHook[A any](ctx context.Context, bindings golem.ApplicationBindings[A], model golem.ModelID, readOperation golem.ReadOperation, hookOperation golem.HookOperation, phase golem.HookPhase, payload any) error {
	if err := golem.RuntimeInvokeHooks(ctx, bindings, model, hookOperation, phase, payload); err != nil {
		return golem.RuntimeReadError(golem.CodeBadUserInput, operationName(readOperation), model, golem.FieldID{}, "read hook rejected the operation", err)
	}
	return nil
}

func invokeReadHookObserved[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, bindings golem.ApplicationBindings[A], model golem.ModelID, readOperation golem.ReadOperation, hookOperation golem.HookOperation, phase golem.HookPhase, payload any) (resultErr error) {
	ctx, observation := beginExecutionObservationPhase(ctx, app, executor, model, observe.KindHook, hookObservationOperation(hookOperation), hookObservationPhase(phase))
	defer func() { finishObservation(observation, resultErr) }()
	return invokeReadHook(ctx, bindings, model, readOperation, hookOperation, phase, payload)
}

func executeRows[P, A, M any](ctx context.Context, app *App[P, A], prepared PreparedRead, descriptor golem.ModelDescriptor[M]) (result []golem.Row[M], resultErr error) {
	if app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "read", prepared.ModelID(), golem.FieldID{}, "read execution is unavailable", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return nil, publicPlanError(prepared, err)
	}
	executed, err := executePlan(ctx, app, prepared.executor, prepared.Operation(), planned)
	if err != nil {
		return nil, err
	}
	result = make([]golem.Row[M], len(executed))
	for index, item := range executed {
		result[index], err = golem.RuntimeTypedReadRow(descriptor, item.row)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

type executedRow struct {
	row    golem.RuntimeModelRow
	record evaluate.Record
	values map[policyir.FieldID]readdecode.Cell
	counts map[readOccurrenceKey]int64
	// batchKeys holds private correlation values appended by a row-shaped
	// batch statement when the caller did not select the child key.
	batchKeys  map[policyir.FieldID]policyir.Value
	correlated map[readOccurrenceKey][]executedRow
}

type readOccurrenceKey struct {
	field      policyir.FieldID
	occurrence readir.OccurrenceID
}

func relationKey(relation readplan.Relation) readOccurrenceKey {
	return readOccurrenceKey{field: relation.FieldID(), occurrence: relation.OccurrenceID()}
}
func countKey(count readplan.RelationCount) readOccurrenceKey {
	return readOccurrenceKey{field: count.FieldID(), occurrence: count.OccurrenceID()}
}

func executePlan[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, operation golem.ReadOperation, planned readplan.Plan) ([]executedRow, error) {
	statement, err := readsql.Render(planned, app.registry, app.provider, app.capabilities)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "read plan could not be rendered", err)
	}
	decoder, err := readdecode.New(planned, app.registry, app.provider)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "read decoder could not be built", err)
	}
	queryer, err := executor.queryerFor(app.database)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "read execution binding is unavailable", err)
	}
	databaseRows, err := queryer.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "read execution failed", err)
	}
	result := make([]executedRow, 0)
	correlatedColumns := statement.CorrelatedColumns()
	relationPlans := make(map[readOccurrenceKey]readplan.Relation, len(planned.Relations()))
	for _, relation := range planned.Relations() {
		relationPlans[relationKey(relation)] = relation
	}
	for databaseRows.Next() {
		scan := decoder.NewScan()
		correlatedSlots := make([]sql.NullString, len(correlatedColumns))
		destinations := scan.Destinations()
		for index := range correlatedSlots {
			destinations = append(destinations, &correlatedSlots[index])
		}
		if err := databaseRows.Scan(destinations...); err != nil {
			databaseRows.Close()
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "read row scan failed", err)
		}
		decoded, err := scan.Decode()
		if err != nil {
			databaseRows.Close()
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "read row decode failed", err)
		}
		values := make(map[policyir.FieldID]readdecode.Cell, len(decoded))
		for _, cell := range decoded {
			values[cell.FieldID()] = cell
		}
		decodedCounts, err := scan.RelationCounts()
		if err != nil {
			databaseRows.Close()
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "relation-count decode failed", err)
		}
		counts := make(map[readOccurrenceKey]int64, len(decodedCounts))
		for _, count := range decodedCounts {
			counts[readOccurrenceKey{field: count.FieldID(), occurrence: count.OccurrenceID()}] = count.Value()
		}
		correlated := make(map[readOccurrenceKey][]executedRow, len(correlatedColumns))
		for index, column := range correlatedColumns {
			if !correlatedSlots[index].Valid {
				databaseRows.Close()
				return nil, fmt.Errorf("P3_RUNTIME_CORRELATED: model=%x relation=%x: correlated to-many JSON is NULL", planned.ModelID(), column.RelationID())
			}
			key := readOccurrenceKey{field: column.FieldID(), occurrence: column.OccurrenceID()}
			relation, ok := relationPlans[key]
			if !ok || relation.RelationID() != column.RelationID() {
				databaseRows.Close()
				return nil, fmt.Errorf("P3_RUNTIME_CORRELATED: model=%x relation=%x: statement relation is absent from plan", planned.ModelID(), column.RelationID())
			}
			decodedRows, decodeErr := readdecode.Correlated(relation.Child(), app.registry, app.provider, correlatedSlots[index].String)
			if decodeErr != nil {
				databaseRows.Close()
				return nil, fmt.Errorf("P3_RUNTIME_CORRELATED: model=%x relation=%x: %w", planned.ModelID(), column.RelationID(), decodeErr)
			}
			children := make([]executedRow, len(decodedRows))
			for childIndex, decodedRow := range decodedRows {
				childValues := make(map[policyir.FieldID]readdecode.Cell, len(decodedRow.Cells()))
				for _, cell := range decodedRow.Cells() {
					childValues[cell.FieldID()] = cell
				}
				childCounts := make(map[readOccurrenceKey]int64, len(decodedRow.RelationCounts()))
				for _, count := range decodedRow.RelationCounts() {
					childCounts[readOccurrenceKey{field: count.FieldID(), occurrence: count.OccurrenceID()}] = count.Value()
				}
				children[childIndex] = executedRow{values: childValues, counts: childCounts}
			}
			if take, present := relation.Child().Take(); present && take < 0 {
				for left, right := 0, len(children)-1; left < right; left, right = left+1, right-1 {
					children[left], children[right] = children[right], children[left]
				}
			}
			correlated[key] = children
		}
		result = append(result, executedRow{values: values, counts: counts, correlated: correlated})
	}
	if err := databaseRows.Err(); err != nil {
		databaseRows.Close()
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "read result stream failed", err)
	}
	if err := databaseRows.Close(); err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), golem.FieldID{}, "read result stream could not be closed", err)
	}
	if statement.ReverseResult() {
		for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
			result[left], result[right] = result[right], result[left]
		}
	}
	if err := enforceDecodedResultLimit(operation, planned, golem.FieldID{}, len(result)); err != nil {
		return nil, err
	}

	return finishPlanRows(ctx, app, executor, operation, planned, result)
}

// finishPlanRows completes one already-decoded row-shaped plan. Keeping this
// separate from statement execution lets root and bounded batch statements use
// exactly the same nested loading, mask evaluation, and public-row builder.
func finishPlanRows[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, operation golem.ReadOperation, planned readplan.Plan, result []executedRow) ([]executedRow, error) {
	relations := planned.Relations()
	relationRows, err := executeRelationViews(ctx, app, executor, operation, planned, result, relations)
	if err != nil {
		return nil, err
	}
	hydrations := planned.Hydrations()
	hydrationRows, err := executeRelationViews(ctx, app, executor, operation, planned, result, hydrations)
	if err != nil {
		return nil, err
	}

	plannedFields := planned.Fields()
	plannedCounts := planned.RelationCounts()
	for rowIndex := range result {
		evaluatorFields := make([]evaluate.Field, 0, len(result[rowIndex].values)+len(hydrations))
		for _, field := range plannedFields {
			cell, ok := result[rowIndex].values[field.FieldID()]
			if ok {
				evaluatorFields = append(evaluatorFields, cell.EvaluatorField())
			}
		}
		// Only the policy-owned hydration view participates in parent mask
		// evaluation. Caller-filtered/paged public relation rows must never
		// change whether an otherwise identical field is visible.
		for hydrationIndex, hydration := range hydrations {
			children := hydrationRows[hydrationIndex][rowIndex]
			childRecords := make([]evaluate.Record, len(children))
			for index, child := range children {
				childRecords[index] = child.record
			}
			var field evaluate.Field
			if hydration.ToMany() {
				field, err = evaluate.ToManyField(hydration.FieldID(), hydration.TargetModelID(), childRecords...)
			} else {
				field, err = evaluate.ToOneField(hydration.FieldID(), hydration.TargetModelID(), childRecords...)
			}
			if err != nil {
				return nil, err
			}
			evaluatorFields = append(evaluatorFields, field)
		}
		record, recordErr := evaluate.NewRecord(planned.ModelID(), evaluatorFields...)
		if recordErr != nil {
			return nil, recordErr
		}
		publicCells := make([]golem.RuntimeReadCell, 0, len(plannedFields)+len(relations))
		publicCounts := make([]golem.RuntimeRelationCountCell, 0, len(plannedCounts))
		publicOccurrences := make([]golem.RuntimeOccurrenceCell, 0, len(relations))
		for _, field := range plannedFields {
			cell, ok := result[rowIndex].values[field.FieldID()]
			if !ok || !cell.Public() {
				continue
			}
			visible, visibilityErr := fieldVisible(field.Mask, record, app.providers)
			if visibilityErr != nil {
				return nil, maskInvariantError(planned.ModelID(), cell.FieldID(), "field", visibilityErr)
			}
			if !visible {
				publicCells = append(publicCells, golem.RuntimeNullReadCell(golem.FieldID(cell.FieldID())))
			} else {
				publicCells = append(publicCells, cell.RuntimeCell())
			}
		}
		for relationIndex, relation := range relations {
			if !relation.Public() {
				continue
			}
			visible, visibilityErr := fieldVisible(relation.Mask, record, app.providers)
			if visibilityErr != nil {
				return nil, maskInvariantError(planned.ModelID(), relation.FieldID(), "relation", visibilityErr)
			}
			if !visible {
				if relation.OccurrenceID() == 0 {
					publicCells = append(publicCells, golem.RuntimeNullReadCell(golem.FieldID(relation.FieldID())))
				} else {
					publicOccurrences = append(publicOccurrences, golem.RuntimeNullOccurrenceCell(golem.FieldID(relation.FieldID()), golem.RuntimeOccurrenceID(relation.OccurrenceID())))
				}
				continue
			}
			children := relationRows[relationIndex][rowIndex]
			if relation.ToMany() {
				rows := make([]golem.RuntimeModelRow, len(children))
				for index, child := range children {
					rows[index] = child.row
				}
				if relation.OccurrenceID() == 0 {
					publicCells = append(publicCells, golem.RuntimeToManyReadCell(golem.FieldID(relation.FieldID()), rows))
				} else {
					publicOccurrences = append(publicOccurrences, golem.RuntimeToManyOccurrenceCell(golem.FieldID(relation.FieldID()), golem.RuntimeOccurrenceID(relation.OccurrenceID()), rows))
				}
			} else if len(children) == 0 {
				if relation.OccurrenceID() == 0 {
					publicCells = append(publicCells, golem.RuntimeNullReadCell(golem.FieldID(relation.FieldID())))
				} else {
					publicOccurrences = append(publicOccurrences, golem.RuntimeNullOccurrenceCell(golem.FieldID(relation.FieldID()), golem.RuntimeOccurrenceID(relation.OccurrenceID())))
				}
			} else {
				if relation.OccurrenceID() == 0 {
					publicCells = append(publicCells, golem.RuntimeToOneReadCell(golem.FieldID(relation.FieldID()), children[0].row))
				} else {
					publicOccurrences = append(publicOccurrences, golem.RuntimeToOneOccurrenceCell(golem.FieldID(relation.FieldID()), golem.RuntimeOccurrenceID(relation.OccurrenceID()), children[0].row))
				}
			}
		}
		for _, count := range plannedCounts {
			value, ok := result[rowIndex].counts[countKey(count)]
			if !ok {
				return nil, fmt.Errorf("P3_RUNTIME_COUNT: model=%x relation=%x: selected relation count was not decoded", planned.ModelID(), count.RelationID())
			}
			visible, visibilityErr := fieldVisible(count.Mask, record, app.providers)
			if visibilityErr != nil {
				return nil, maskInvariantError(planned.ModelID(), count.FieldID(), "relation count", visibilityErr)
			}
			if !visible {
				if count.OccurrenceID() == 0 {
					publicCounts = append(publicCounts, golem.RuntimeNullRelationCountCell(golem.FieldID(count.FieldID()), golem.RelationID(count.RelationID())))
				} else {
					publicCounts = append(publicCounts, golem.RuntimeNullRelationCountOccurrenceCell(golem.FieldID(count.FieldID()), golem.RelationID(count.RelationID()), golem.RuntimeOccurrenceID(count.OccurrenceID())))
				}
			} else {
				if count.OccurrenceID() == 0 {
					publicCounts = append(publicCounts, golem.RuntimePresentRelationCountCell(golem.FieldID(count.FieldID()), golem.RelationID(count.RelationID()), value))
				} else {
					publicCounts = append(publicCounts, golem.RuntimePresentRelationCountOccurrenceCell(golem.FieldID(count.FieldID()), golem.RelationID(count.RelationID()), golem.RuntimeOccurrenceID(count.OccurrenceID()), value))
				}
			}
		}
		result[rowIndex].record = record
		result[rowIndex].row, err = golem.RuntimeModelReadRowWithOccurrences(golem.ModelID(planned.ModelID()), publicCells, publicCounts, publicOccurrences)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func executeRelationViews[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, operation golem.ReadOperation, parent readplan.Plan, result []executedRow, relations []readplan.Relation) ([][][]executedRow, error) {
	relationRows := make([][][]executedRow, len(relations))
	for relationIndex, relation := range relations {
		relationRows[relationIndex] = make([][]executedRow, len(result))
		endpoint, ok := app.registry.RelationEndpoint(golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), golem.RelationID(relation.RelationID()))
		if !ok || policyir.ModelID(endpoint.TargetModelID()) != relation.TargetModelID() {
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), "relation plan no longer matches the schema", nil)
		}
		forcedStrategy, forced := forcedRelationLoadStrategy(ctx)
		if !forced && readsql.ChooseRelationStrategy(parent, relation, app.registry, app.provider) == readsql.RelationCorrelated && correlatedPayloadAvailable(result, relation) {
			children, correlatedErr := finishCorrelatedRelation(ctx, app, executor, operation, relation, result)
			if correlatedErr != nil {
				return nil, correlatedErr
			}
			if !relation.ToMany() {
				for _, rows := range children {
					if len(rows) > 1 {
						return nil, fmt.Errorf("P3_RUNTIME_CARDINALITY: model=%x field=%x: to-one relation returned more than one row", parent.ModelID(), relation.FieldID())
					}
				}
			}
			relationRows[relationIndex] = children
			continue
		}
		if relation.ToMany() {
			strategy := relationLoadBatched
			if forced {
				strategy = forcedStrategy
			}
			var children [][]executedRow
			var batchErr error
			switch strategy {
			case relationLoadBatched:
				children, batchErr = executeToManyBatch(ctx, app, executor, operation, parent, relation, endpoint, result)
			case relationLoadCorrelatedOracle:
				children, batchErr = executeToManyCorrelatedOracle(ctx, app, executor, operation, parent, relation, endpoint, result)
			default:
				batchErr = fmt.Errorf("P3_RUNTIME_RELATION_STRATEGY: unknown relation load strategy")
			}
			if batchErr != nil {
				return nil, batchErr
			}
			relationRows[relationIndex] = children
			continue
		}
		children, batchErr := executeToOneBatch(ctx, app, executor, operation, parent, relation, endpoint, result)
		if batchErr != nil {
			return nil, batchErr
		}
		for rowIndex := range children {
			if len(children[rowIndex]) > 1 {
				return nil, fmt.Errorf("P3_RUNTIME_CARDINALITY: model=%x field=%x: to-one relation returned more than one row", parent.ModelID(), relation.FieldID())
			}
		}
		relationRows[relationIndex] = children
	}
	return relationRows, nil
}

func fieldVisible(mask func() (policyir.Condition, bool), record evaluate.Record, providers policyir.ProviderSet) (bool, error) {
	condition, conditional := mask()
	if !conditional {
		return true, nil
	}
	visible, err := evaluate.Condition(condition, record, providers)
	return visible, err
}

func maskInvariantError(model policyir.ModelID, field policyir.FieldID, kind string, cause error) error {
	return fmt.Errorf("P3_RUNTIME_MASK: model=%x field=%x: %s authorization dependencies were incomplete: %w", model, field, kind, cause)
}

func relationCorrelation[P, A any](app *App[P, A], child readplan.Plan, endpoint schema.RelationEndpoint, parent map[policyir.FieldID]readdecode.Cell) (policyir.Condition, bool, error) {
	resolver := policysql.SchemaResolver(app.registry)
	conditions := make([]policyir.Condition, 0, len(endpoint.Correlation()))
	for _, pair := range endpoint.Correlation() {
		cell, ok := parent[policyir.FieldID(pair.ParentFieldID())]
		if !ok {
			return policyir.Condition{}, false, fmt.Errorf("parent correlation field %x was not loaded", pair.ParentFieldID())
		}
		if cell.IsNull() {
			return policyir.Condition{}, true, nil
		}
		value, ok := cell.PolicyValue()
		if !ok {
			return policyir.Condition{}, false, fmt.Errorf("parent correlation field %x has no scalar policy value", pair.ParentFieldID())
		}
		field, ok := resolver.Field(app.provider, child.ModelID(), policyir.FieldID(pair.ChildFieldID()))
		if !ok {
			return policyir.Condition{}, false, fmt.Errorf("child correlation field %x is absent", pair.ChildFieldID())
		}
		operand, operandErr := policyir.OneOperand(value)
		if operandErr != nil {
			return policyir.Condition{}, false, operandErr
		}
		requirements, shapeErr := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
		if shapeErr != nil {
			return policyir.Condition{}, false, shapeErr
		}
		condition, conditionErr := policyir.NewScalar(child.ModelID(), policyir.FieldID(pair.ChildFieldID()), field.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if conditionErr != nil {
			return policyir.Condition{}, false, conditionErr
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 0 {
		return policyir.Condition{}, false, fmt.Errorf("relation has no correlation pairs")
	}
	if len(conditions) == 1 {
		return conditions[0], false, nil
	}
	result, err := policyir.NewLogical(child.ModelID(), policyir.LogicalAnd, conditions)
	return result, false, err
}

func executeCount[P, A any](ctx context.Context, app *App[P, A], prepared PreparedRead) (result int64, resultErr error) {
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return 0, publicPlanError(prepared, err)
	}
	statement, err := readsql.Render(planned, app.registry, app.provider, app.capabilities)
	if err != nil {
		return 0, golem.RuntimeReadError(golem.CodeBadUserInput, "count", prepared.ModelID(), golem.FieldID{}, "count plan could not be rendered", err)
	}
	queryer, err := prepared.executor.queryerFor(app.database)
	if err != nil {
		return 0, golem.RuntimeReadError(golem.CodeBadUserInput, "count", prepared.ModelID(), golem.FieldID{}, "count execution binding is unavailable", err)
	}
	if err := sqlx.GetContext(ctx, queryer, &result, statement.SQL(), statement.Args()...); err != nil {
		return 0, golem.RuntimeReadError(golem.CodeBadUserInput, "count", prepared.ModelID(), golem.FieldID{}, "count execution failed", err)
	}
	return result, nil
}

func preparePlan(prepared PreparedRead, registry *schema.Registry, limits readplan.Limits) (readplan.Plan, error) {
	if prepared.system {
		return readplan.System(prepared.request, registry, limits)
	}
	return readplan.Caller(prepared.request, registry, prepared.policies, limits)
}

func enforceDecodedResultLimit(operation golem.ReadOperation, planned readplan.Plan, field golem.FieldID, rows int) error {
	maximum := planned.ResultLimit()
	if maximum == 0 || rows <= maximum {
		return nil
	}
	message := "root read limit exceeded"
	if field != (golem.FieldID{}) {
		message = "relation fanout limit exceeded"
	}
	return golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(planned.ModelID()), field, message, nil)
}

func publicPlanError(prepared PreparedRead, cause error) error {
	var failure *readplan.Error
	if errors.As(cause, &failure) && (failure.Code == readplan.CodeField || failure.Code == readplan.CodePolicy) {
		return golem.RuntimeReadError(golem.CodeForbidden, operationName(prepared.Operation()), prepared.ModelID(), golem.FieldID(failure.Field), "read is not permitted", cause)
	}
	return golem.RuntimeReadError(golem.CodeBadUserInput, operationName(prepared.Operation()), prepared.ModelID(), golem.FieldID{}, "read request could not be planned", cause)
}

func validateInventory[A any](bindings golem.ApplicationBindings[A], descriptors golem.ApplicationDescriptors, registry *schema.Registry) error {
	models := descriptors.Models()
	if len(models) == 0 {
		return fmt.Errorf("P3_RUNTIME_INVENTORY: no generated model descriptors")
	}
	modelSet := make(map[golem.ModelID]struct{}, len(models))
	for _, model := range models {
		id := model.ModelID()
		if id == (golem.ModelID{}) || !registry.HasModel(id) {
			return fmt.Errorf("P3_RUNTIME_INVENTORY: descriptor model is absent from schema")
		}
		if _, duplicate := modelSet[id]; duplicate {
			return fmt.Errorf("P3_RUNTIME_INVENTORY: duplicate descriptor model")
		}
		modelSet[id] = struct{}{}
	}
	policies := bindings.PolicyInventory()
	if len(policies) != len(modelSet) {
		return fmt.Errorf("P3_RUNTIME_INVENTORY: every model requires exactly one policy")
	}
	for _, model := range policies {
		if _, ok := modelSet[model]; !ok {
			return fmt.Errorf("P3_RUNTIME_INVENTORY: policy model is absent from descriptors")
		}
	}
	for _, hook := range bindings.RuntimeHookInventory() {
		if _, ok := modelSet[hook.Model]; !ok {
			return fmt.Errorf("P3_RUNTIME_INVENTORY: hook model is absent from descriptors")
		}
		switch hook.Operation {
		case golem.HookFindOne, golem.HookFindFirst, golem.HookFindMany,
			golem.HookCreate, golem.HookUpdate, golem.HookDelete, golem.HookUpdateMany, golem.HookDeleteMany:
		default:
			return fmt.Errorf("P3_RUNTIME_INVENTORY: hook operation is invalid")
		}
		switch hook.Phase {
		case golem.HookBefore, golem.HookAfter, golem.HookAfterCommit:
		default:
			return fmt.Errorf("P3_RUNTIME_INVENTORY: hook phase is invalid")
		}
	}
	return nil
}

func providerSet(registry *schema.Registry) (policyir.ProviderSet, error) {
	values := make([]policyir.Provider, 0, 2)
	for _, provider := range registry.Providers() {
		value, ok := internalProvider(provider)
		if !ok {
			return 0, fmt.Errorf("unknown provider %q", provider)
		}
		values = append(values, value)
	}
	return policyir.NewProviderSet(values...)
}

func proveCapabilities(ctx context.Context, database *sqlx.DB, provider policyir.Provider, fingerprint [32]byte, poolWidth ...int) (policysql.CapabilityProof, error) {
	width := database.Stats().MaxOpenConnections
	if width < 1 {
		width = 2
	}
	if len(poolWidth) != 0 {
		width = poolWidth[0]
	}
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.New().VerifiedPoolPolicyCapabilityProof(ctx, database, fingerprint, width)
	case policyir.ProviderPostgreSQL:
		return postgresprovider.New().VerifiedPoolPolicyCapabilityProof(ctx, database, fingerprint, width)
	default:
		return policysql.CapabilityProof{}, fmt.Errorf("unknown provider %d", provider)
	}
}

func selectedPhysicalSchema(bundle golem.SchemaBundle, provider golem.Provider) (physical.PhysicalSchema, error) {
	for _, document := range bundle.Providers() {
		if document.Provider() != provider {
			continue
		}
		schemaDocument := document.Schema()
		return physical.CanonicalDecodeVerified(schemaDocument.Bytes(), physical.Digest(schemaDocument.Fingerprint()), physical.Digest(document.SystemFingerprint()))
	}
	return physical.PhysicalSchema{}, fmt.Errorf("provider %q has no physical schema", provider)
}

func verifyPhysical(ctx context.Context, database *sqlx.DB, provider policyir.Provider, expected physical.PhysicalSchema) error {
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.New().Verify(ctx, database, expected)
	case policyir.ProviderPostgreSQL:
		return postgresprovider.New().Verify(ctx, database, expected)
	default:
		return fmt.Errorf("unknown provider %d", provider)
	}
}

func internalProvider(provider golem.Provider) (policyir.Provider, bool) {
	switch provider {
	case golem.SQLite:
		return policyir.ProviderSQLite, true
	case golem.PostgreSQL:
		return policyir.ProviderPostgreSQL, true
	default:
		return 0, false
	}
}

func externalOperation(operation readir.Operation) golem.ReadOperation {
	switch operation {
	case readir.FindUnique:
		return golem.ReadFindUnique
	case readir.FindFirst:
		return golem.ReadFindFirst
	case readir.FindMany:
		return golem.ReadFindMany
	case readir.Count:
		return golem.ReadCount
	default:
		return 0
	}
}

func operationName(operation golem.ReadOperation) string {
	switch operation {
	case golem.ReadFindUnique:
		return "findUnique"
	case golem.ReadFindFirst:
		return "findFirst"
	case golem.ReadFindMany:
		return "findMany"
	case golem.ReadCount:
		return "count"
	default:
		return "read"
	}
}
