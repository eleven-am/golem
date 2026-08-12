package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/generate/publication"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/migration/explain"
	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/migration/workflow"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type approvalFlags []string

const migrationOutputFormatVersion uint16 = 1

func (values *approvalFlags) String() string { return strings.Join(*values, ",") }

func (values *approvalFlags) Set(value string) error {
	if value == "" {
		return errors.New("approval operation ID must not be empty")
	}
	*values = append(*values, value)
	return nil
}

type migrationNewOutput struct {
	FormatVersion uint16                `json:"formatVersion"`
	MigrationID   migration.MigrationID `json:"migrationId"`
	Providers     []ir.Provider         `json:"providers"`
	Changed       []string              `json:"changed"`
	Pending       bool                  `json:"pending,omitempty"`
	Draft         string                `json:"draft,omitempty"`
}

type migrationBackfillAttachOutput struct {
	FormatVersion uint16                `json:"formatVersion"`
	MigrationID   migration.MigrationID `json:"migrationId"`
	Provider      ir.Provider           `json:"provider"`
	Changed       []string              `json:"changed"`
}

type migrationApplyOutput struct {
	FormatVersion uint16                  `json:"formatVersion"`
	Provider      ir.Provider             `json:"provider"`
	Applied       []migration.MigrationID `json:"applied"`
}

func runMigration(ctx context.Context, directory string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeError(stderr, errors.New(usage))
		return 2
	}
	switch args[0] {
	case "new":
		return runMigrationNew(ctx, directory, args[1:], stdout, stderr)
	case "plan":
		return runMigrationPlan(ctx, directory, args[1:], stdout, stderr)
	case "apply":
		return runMigrationApply(ctx, directory, args[1:], stdout, stderr)
	case "backfill":
		if len(args) < 2 || args[1] != "attach" {
			writeError(stderr, errors.New(usage))
			return 2
		}
		return runMigrationBackfillAttach(ctx, directory, args[2:], stdout, stderr)
	default:
		writeError(stderr, errors.New(usage))
		return 2
	}
}

func runMigrationNew(ctx context.Context, directory string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migration new", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "canonical migration slug")
	schemaPattern := flags.String("schema", ".", "Go package pattern containing the schema root")
	rootName := flags.String("root", "DefineSchema", "schema root function name")
	migrations := flags.String("migrations", workflow.DefaultRoot, "module-relative migration history root")
	var approvals approvalFlags
	flags.Var(&approvals, "approve", "exact destructive operation ID to approve (repeatable)")
	if err := flags.Parse(args); err != nil || *name == "" || *schemaPattern == "" || flags.NArg() != 0 {
		writeError(stderr, errors.New(usage))
		return 2
	}
	migrationRoot, err := canonicalMigrationRoot(*migrations)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	module, err := resolveModule(directory)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if err := publication.Recover(ctx, module.directory); err != nil {
		writeError(stderr, err)
		return 1
	}
	previousModel, err := optionalMigrationHead(module.directory, migrationRoot, false)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	request := pipelineRequest(directory, commonOptions{schemaPattern: *schemaPattern, root: *rootName}, modelcodegen.PackageSpec{}, nil)
	request.Compile.PreviousModel = previousModel
	result, err := pipeline.Build(ctx, request)
	if err != nil {
		return writeBuildFailure(stdout, stderr, err)
	}
	providers, err := migrationProviders(result.Providers)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	approved := make([]migration.OperationID, len(approvals))
	for index, value := range approvals {
		approved[index] = migration.OperationID(value)
	}
	prepared, err := workflow.PrepareNew(ctx, workflow.NewRequest{
		ModuleDir: module.directory, Root: migrationRoot, Name: *name,
		ModelFingerprint: result.ModelFingerprint, ContractFingerprint: result.ContractFingerprint,
		Model: result.Compilation.Model, PreviousModel: previousModel, Providers: providers, Approvals: approved,
	})
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if prepared.Pending != nil {
		draft, err := workflow.WritePendingBackfill(module.directory, *prepared.Pending)
		if err != nil {
			writeError(stderr, err)
			return 1
		}
		if err := encodeJSON(stdout, migrationNewOutput{FormatVersion: migrationOutputFormatVersion, MigrationID: prepared.MigrationID, Providers: prepared.Providers, Changed: []string{draft}, Pending: true, Draft: draft}); err != nil {
			writeError(stderr, err)
			return 1
		}
		return 0
	}
	published, err := publication.Apply(ctx, publication.Request{
		ModuleDir: module.directory, ManifestPath: prepared.PublicationPath,
		Prospective: prepared.Prospective, Mode: publication.ModePublish,
	})
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if err := encodeJSON(stdout, migrationNewOutput{FormatVersion: migrationOutputFormatVersion, MigrationID: prepared.MigrationID, Providers: prepared.Providers, Changed: published.Changed}); err != nil {
		writeError(stderr, err)
		return 1
	}
	return 0
}

func runMigrationBackfillAttach(ctx context.Context, directory string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migration backfill attach", flag.ContinueOnError)
	flags.SetOutput(stderr)
	migrationID := flags.String("migration", "", "exact pending migration ID")
	field := flags.String("field", "", "exact Model.Field backfill target")
	filename := flags.String("file", "", "reviewed PostgreSQL SQL file")
	migrations := flags.String("migrations", workflow.DefaultRoot, "module-relative migration history root")
	if err := flags.Parse(args); err != nil || *migrationID == "" || *field == "" || *filename == "" || flags.NArg() != 0 {
		writeError(stderr, errors.New(usage))
		return 2
	}
	migrationRoot, err := canonicalMigrationRoot(*migrations)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	module, err := resolveModule(directory)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if err := publication.Recover(ctx, module.directory); err != nil {
		writeError(stderr, err)
		return 1
	}
	reviewedPath := *filename
	if !filepath.IsAbs(reviewedPath) {
		reviewedPath = filepath.Join(directory, reviewedPath)
	}
	reviewed, err := os.ReadFile(reviewedPath)
	if err != nil {
		writeError(stderr, errors.New("reviewed backfill file could not be read"))
		return 1
	}
	render, err := providerRenderer(ir.PostgreSQL)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	prepared, err := workflow.PrepareBackfillAttach(ctx, workflow.BackfillAttachRequest{ModuleDir: module.directory, Root: migrationRoot, MigrationID: migration.MigrationID(*migrationID), Field: *field, SQL: reviewed, Render: render})
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	planText, err := explain.MarshalText(prepared.Report)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if _, err := stdout.Write(planText); err != nil {
		writeError(stderr, errors.New("migration plan output could not be written"))
		return 1
	}
	published, err := publication.Apply(ctx, publication.Request{
		ModuleDir: module.directory, ManifestPath: prepared.PublicationPath,
		Prospective: prepared.Prospective, Mode: publication.ModePublish,
	})
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if err := workflow.RemovePendingBackfill(module.directory, prepared.PendingPath, prepared.PendingSHA256); err != nil {
		writeError(stderr, err)
		return 1
	}
	_ = published
	return 0
}

func runMigrationApply(ctx context.Context, directory string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migration apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	providerName := flags.String("provider", "", "database provider (sqlite or postgresql)")
	dsn := flags.String("dsn", "", "explicit provider data source name")
	migrations := flags.String("migrations", workflow.DefaultRoot, "module-relative migration history root")
	if err := flags.Parse(args); err != nil || *providerName == "" || *dsn == "" || flags.NArg() != 0 {
		writeError(stderr, errors.New(usage))
		return 2
	}
	providerID := ir.Provider(*providerName)
	if providerID != ir.SQLite && providerID != ir.PostgreSQL {
		writeError(stderr, fmt.Errorf("unsupported migration provider %q", *providerName))
		return 2
	}
	migrationRoot, err := canonicalMigrationRoot(*migrations)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	module, err := resolveModule(directory)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if err := publication.Recover(ctx, module.directory); err != nil {
		writeError(stderr, err)
		return 1
	}
	providerIDs, err := workflow.PublishedProviders(module.directory, migrationRoot)
	if err != nil {
		writeError(stderr, fmt.Errorf("read reviewed migration history: %w", err))
		return 1
	}
	providers, err := reviewedMigrationProviders(providerIDs)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	state, err := workflow.Load(ctx, module.directory, migrationRoot, providers)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	history, exists := state.Histories[providerID]
	if !exists {
		writeError(stderr, fmt.Errorf("reviewed migration history does not declare provider %s", providerID))
		return 1
	}
	applied, err := applyReviewedHistory(ctx, providerID, *dsn, history)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if err := encodeJSON(stdout, migrationApplyOutput{FormatVersion: migrationOutputFormatVersion, Provider: providerID, Applied: applied}); err != nil {
		writeError(stderr, err)
		return 1
	}
	return 0
}

func migrationProviders(results []pipeline.ProviderResult) ([]workflow.Provider, error) {
	providers := make([]workflow.Provider, 0, len(results))
	for _, result := range results {
		render, err := providerRenderer(result.Provider.Provider)
		if err != nil {
			return nil, err
		}
		providers = append(providers, workflow.Provider{Result: result, Render: render})
	}
	return providers, nil
}

func reviewedMigrationProviders(ids []ir.Provider) ([]workflow.Provider, error) {
	providers := make([]workflow.Provider, 0, len(ids))
	for _, id := range ids {
		render, err := providerRenderer(id)
		if err != nil {
			return nil, err
		}
		result := pipeline.ProviderResult{}
		var providerManifest physical.ProviderManifest
		switch id {
		case ir.SQLite:
			providerManifest = sqlite.New().Manifest()
		case ir.PostgreSQL:
			providerManifest = postgresql.New().Manifest()
		}
		normalized, normalizeErr := physical.Normalize(physical.PhysicalSchema{
			Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
			Provider: providerManifest, Namespace: physical.Namespace{Name: "main"},
		})
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		result.Provider = normalized.Provider
		providers = append(providers, workflow.Provider{Result: result, Render: render})
	}
	return providers, nil
}

func providerRenderer(provider ir.Provider) (func(migration.ManifestEntry) ([]byte, error), error) {
	switch provider {
	case ir.SQLite:
		value := sqlite.New()
		return func(entry migration.ManifestEntry) ([]byte, error) {
			script, err := value.RenderMigration(entry)
			return []byte(script.SQL()), err
		}, nil
	case ir.PostgreSQL:
		value := postgresql.New()
		return func(entry migration.ManifestEntry) ([]byte, error) {
			script, renderErr := value.RenderMigration(entry)
			return []byte(script.SQL()), renderErr
		}, nil
	default:
		return nil, fmt.Errorf("migration provider %q is unsupported", provider)
	}
}

func applyReviewedHistory(ctx context.Context, provider ir.Provider, dsn string, history workflow.History) ([]migration.MigrationID, error) {
	switch provider {
	case ir.SQLite:
		value := sqlite.New()
		database, _, err := value.Open(ctx, dsn)
		if err != nil {
			return nil, errors.New("open sqlite database: connection or capability validation failed")
		}
		defer database.Close()
		return applyPending(ctx, database, history, value.ReadLedger, value.ApplyMigration)
	case ir.PostgreSQL:
		value := postgresql.New()
		database, _, err := value.Open(ctx, dsn)
		if err != nil {
			return nil, errors.New("open postgresql database: connection or capability validation failed")
		}
		defer database.Close()
		return applyPending(ctx, database, history, value.ReadLedger, value.ApplyMigration)
	default:
		return nil, fmt.Errorf("migration provider %q is unsupported", provider)
	}
}

func applyPending(
	ctx context.Context,
	database *sqlx.DB,
	history workflow.History,
	read func(context.Context, *sqlx.DB) ([]migration.LedgerEntry, error),
	apply func(context.Context, *sqlx.DB, migration.Manifest, map[string][]byte) error,
) ([]migration.MigrationID, error) {
	ledger, err := read(ctx, database)
	if err != nil {
		return nil, err
	}
	if err := verifyLedgerPrefix(history.Manifest, ledger); err != nil {
		return nil, err
	}
	initialLength := len(ledger)
	currentLength := initialLength
	for currentLength < len(history.Manifest.Entries) {
		attempted := history.Manifest.Entries[currentLength].ID
		applyErr := apply(ctx, database, history.Manifest, history.Files)
		migrationfailpoint.Reach(ctx, "during_final_verification")
		current, readErr := read(ctx, database)
		if readErr != nil {
			return nil, fmt.Errorf("read database migration history after attempting %s: %w", attempted, readErr)
		}
		if verifyErr := verifyLedgerPrefix(history.Manifest, current); verifyErr != nil {
			return nil, verifyErr
		}
		if len(current) < currentLength {
			return nil, fmt.Errorf("database migration ledger moved backwards while applying %s", attempted)
		}
		if len(current) == currentLength {
			if applyErr != nil {
				return nil, fmt.Errorf("apply migration %s: %w", attempted, applyErr)
			}
			return nil, fmt.Errorf("apply migration %s reported success without advancing the verified ledger", attempted)
		}
		// A competing runner may have won the exact-next race. Its advancement is
		// success only because the newly read ledger proves the immutable reviewed
		// prefix; no provider error text participates in this decision.
		currentLength = len(current)
	}
	applied := make([]migration.MigrationID, currentLength-initialLength)
	for index := range applied {
		applied[index] = history.Manifest.Entries[initialLength+index].ID
	}
	return applied, nil
}

func verifyLedgerPrefix(manifest migration.Manifest, ledger []migration.LedgerEntry) error {
	if len(ledger) > len(manifest.Entries) {
		return fmt.Errorf("database migration ledger is ahead of reviewed history")
	}
	prefix := manifest
	prefix.Entries = append([]migration.ManifestEntry(nil), manifest.Entries[:len(ledger)]...)
	if err := migration.VerifyLedger(prefix, ledger); err != nil {
		return fmt.Errorf("verify database migration history: %w", err)
	}
	return nil
}

func canonicalMigrationRoot(value string) (string, error) {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, `\`) {
		return "", fmt.Errorf("migration root %q must be canonical and module-relative", value)
	}
	canonical := path.Clean(value)
	if canonical == "." || canonical == ".." || strings.HasPrefix(canonical, "../") || canonical != value {
		return "", fmt.Errorf("migration root %q must be canonical and module-relative", value)
	}
	return canonical, nil
}

func migrationPublicationExists(moduleDir, root string) (bool, error) {
	rootPath := filepath.Join(moduleDir, filepath.FromSlash(root))
	_, err := os.Stat(filepath.Join(rootPath, workflow.PublicationFilename))
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entries, readErr := os.ReadDir(rootPath)
	if errors.Is(readErr, os.ErrNotExist) {
		return false, nil
	}
	if readErr != nil {
		return false, readErr
	}
	if len(entries) != 0 {
		return false, fmt.Errorf("migration root %q is nonempty but has no %s integrity manifest", root, workflow.PublicationFilename)
	}
	return false, nil
}

func optionalMigrationHead(moduleDir, root string, required bool) (*ir.ModelIR, error) {
	exists, err := migrationPublicationExists(moduleDir, root)
	if err != nil {
		return nil, err
	}
	if !exists {
		if required {
			return nil, fmt.Errorf("migration history %q does not exist", root)
		}
		return nil, nil
	}
	return workflow.ReadModelHead(moduleDir, root)
}

func recoverMigrationHistoryIfPresent(ctx context.Context, moduleDir, root string, required bool) error {
	// Publication recovery is global to the module and journal-routed. It must
	// precede even history-presence validation so an interrupted first migration
	// (or another publication owner) cannot be stranded by an early return.
	if err := publication.Recover(ctx, moduleDir); err != nil {
		return err
	}
	rootPath := filepath.Join(moduleDir, filepath.FromSlash(root))
	if _, err := os.Stat(rootPath); errors.Is(err, os.ErrNotExist) {
		if required {
			return fmt.Errorf("migration history %q does not exist", root)
		}
		return nil
	} else if err != nil {
		return err
	}
	exists, err := migrationPublicationExists(moduleDir, root)
	if err != nil {
		return err
	}
	if required && !exists {
		return fmt.Errorf("migration history %q does not exist", root)
	}
	return nil
}
