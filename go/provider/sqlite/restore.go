package sqlite

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/provider"
)

type RestoreMode string

const (
	// RestoreResumeAtLeastOnce permits archived work to run again, including
	// external effects that succeeded before the archive recorded completion.
	RestoreResumeAtLeastOnce RestoreMode = "resume-at-least-once"
	// RestoreAbandonAndResync cancels archived unfinished jobs and retires
	// archived undelivered events. Domain state and external effects still need
	// explicit reconciliation; fresh clients must resynchronize after restore.
	RestoreAbandonAndResync RestoreMode = "abandon-and-resync"
)

type RestoreConfig struct {
	Archive, Destination   string
	ArchiveBundle          golem.SchemaBundle
	TargetBundle           golem.SchemaBundle
	HistoricalEventBundles []golem.SchemaBundle
	Mode                   RestoreMode
}

// RestoreSession owns a private copy used for offline migration and domain
// reconciliation. It does not start serving, queue workers or event publishers.
type RestoreSession struct{ state *providerhandle.SQLiteRestore }

// PrepareRestore requires the deployment owner to stop all old processes,
// traffic, workers, publishers, CDC adapters, readers and writers, and prevent
// automatic restart until publication and fresh application startup succeed.
// Returning from a worker's shutdown grace or closing one database handle does
// not prove that precondition. The same exclusion applies to staging work.
//
// Archive must be an immutable standalone regular SQLite file without sidecars.
// Destination is a caller-owned offline local file namespace in a trusted,
// stable directory. Existing destinations must be closed and checkpointed
// through SQLite first; sidecars, symlinks and observed changes are refused.
// The caller's exclusion covers the interval between namespace checks and
// publication; those separate checks do not establish a filesystem fence.
// The caller owns archive extraction and application assets. This API provides
// no network-filesystem guarantee and no mechanical whole-fleet fence.
//
// Both bundles are explicit: ArchiveBundle verifies the copied image, and
// TargetBundle verifies publication. TargetBundle must declare durable data
// identity. If they differ, ArchiveBundle is registered automatically for fact
// decoding; HistoricalEventBundles supplies other retained producer schemas and
// must not duplicate either bundle. Unsupported codecs or incomplete fact
// groups fail before the copy is exposed. Resume additionally requires active
// event compatibility for archived groups that will be delivered again.
//
// Archived work is reconciled exactly once during preparation under Mode.
// Then use StagePath for reviewed migration commands, and Database with a fresh
// generated application for explicit domain/external-effect reconciliation and
// derived-state rebuilding. Reconciliation-created work is not implicitly
// abandoned. Row versions, counters and immutable fact bytes are never reset.
// Recognized old queue layouts need an explicit queue storage upgrade on the
// extracted offline copy before preparation; restore does not infer an upgrade.
// Close every staging application operation and provider handle before Publish.
// Close the session to discard an unpublished copy.
func PrepareRestore(ctx context.Context, config RestoreConfig) (*RestoreSession, error) {
	state, err := providerhandle.PrepareSQLiteRestore(ctx, providerhandle.SQLiteRestoreConfig{
		Archive: config.Archive, Destination: config.Destination,
		ArchiveBundle: config.ArchiveBundle, TargetBundle: config.TargetBundle,
		HistoricalEventBundles: config.HistoricalEventBundles,
		Mode:                   internalsqlite.RestoreWorkMode(config.Mode),
	})
	if err != nil {
		return nil, err
	}
	return &RestoreSession{state: state}, nil
}

// Database returns the private provider handle for generated offline domain
// reconciliation. It is not a serving handle; no application may outlive it.
func (restore *RestoreSession) Database() *provider.Database {
	if restore == nil || restore.state == nil {
		return nil
	}
	return (*provider.Database)(restore.state.Database())
}

// StagePath identifies only the owned private copy for reviewed offline CLI
// migrations. Every separately opened handle must close before Publish.
func (restore *RestoreSession) StagePath() string {
	if restore == nil || restore.state == nil {
		return ""
	}
	return restore.state.StagePath()
}

// Publish verifies the final catalog, exact reviewed ledger and complete
// retained fact groups, establishes a fresh durable incarnation, then atomically
// publishes the local file. Existing destinations are replaced only under the
// offline ownership precondition. Pending jobs are never implicitly deleted.
//
// Pre-publication failure preserves the destination. After publication, a
// finalization failure returns the new identity and provider.CodeRestorePublished;
// keep serving stopped and inspect the destination instead of assuming rollback.
// On success, open a fresh provider handle and generated app, then enable work
// and traffic. Authenticated client discovery resets caches and continuations.
func (restore *RestoreSession) Publish(ctx context.Context) (golem.DataIncarnation, error) {
	if restore == nil || restore.state == nil {
		return (*providerhandle.SQLiteRestore)(nil).Publish(ctx)
	}
	return restore.state.Publish(ctx)
}

// Close discards private staging and is safe to repeat. It never removes or
// rolls back a destination that has already been published.
func (restore *RestoreSession) Close() error {
	if restore == nil || restore.state == nil {
		return nil
	}
	return restore.state.Close()
}
