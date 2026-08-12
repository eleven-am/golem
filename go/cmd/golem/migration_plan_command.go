package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"regexp"
	"sort"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/migration/explain"
	"github.com/eleven-am/golem/go/internal/migration/workflow"
)

type migrationPlanOptions struct {
	schema            string
	root              string
	migrations        string
	migrationID       string
	provider          string
	json              bool
	schemaExplicit    bool
	rootExplicit      bool
	migrationExplicit bool
	providerExplicit  bool
}

func runMigrationPlan(ctx context.Context, directory string, args []string, stdout, stderr io.Writer) (code int) {
	options, ok := parseMigrationPlanOptions(args)
	if !ok {
		writeError(stderr, errors.New(usage))
		return 2
	}
	migrationRoot, err := canonicalMigrationRoot(options.migrations)
	if err != nil {
		writeError(stderr, errors.New("migration plan root is invalid"))
		return 1
	}
	module, err := resolveModule(directory)
	if err != nil {
		writeError(stderr, errors.New("migration plan module could not be resolved"))
		return 1
	}
	before, err := snapshotMigrationPlanTree(module.directory)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	code = 1
	defer func() {
		panicked := recover() != nil
		unchangedErr := verifyMigrationPlanTree(module.directory, before)
		if unchangedErr != nil {
			migrationPlanWriteError(stderr, unchangedErr)
			code = 1
		}
		if panicked {
			migrationPlanWriteError(stderr, errors.New("migration plan output could not be written"))
			code = 1
		}
	}()

	report, err := buildMigrationPlanReport(ctx, directory, module.directory, migrationRoot, options)
	if err != nil {
		var diagnostics *pipeline.DiagnosticsError
		if errors.As(err, &diagnostics) {
			if unchangedErr := verifyMigrationPlanTree(module.directory, before); unchangedErr != nil {
				migrationPlanWriteError(stderr, unchangedErr)
				return 1
			}
			return writeMigrationPlanBuildFailure(stdout, stderr, module.directory, options, diagnostics)
		}
		migrationPlanWriteError(stderr, redactMigrationPlanError(module.directory, err))
		return 1
	}
	if options.provider != "" {
		report, err = explain.FilterProvider(report, ir.Provider(options.provider))
		if err != nil {
			migrationPlanWriteError(stderr, err)
			return 1
		}
	}
	var encoded []byte
	if options.json {
		encoded, err = explain.MarshalJSON(report)
	} else {
		encoded, err = explain.MarshalText(report)
	}
	if err != nil {
		migrationPlanWriteError(stderr, err)
		return 1
	}
	if err := verifyMigrationPlanTree(module.directory, before); err != nil {
		migrationPlanWriteError(stderr, err)
		return 1
	}
	written, err := stdout.Write(encoded)
	if err != nil || written != len(encoded) {
		migrationPlanWriteError(stderr, errors.New("migration plan output could not be written"))
		return 1
	}
	return 0
}

func parseMigrationPlanOptions(args []string) (migrationPlanOptions, bool) {
	flags := flag.NewFlagSet("migration plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := migrationPlanOptions{}
	flags.StringVar(&options.schema, "schema", ".", "Go package pattern containing the schema root")
	flags.StringVar(&options.root, "root", "DefineSchema", "schema root function name")
	flags.StringVar(&options.migrations, "migrations", workflow.DefaultRoot, "module-relative migration history root")
	flags.StringVar(&options.migrationID, "migration", "", "exact reviewed migration ID")
	flags.StringVar(&options.provider, "provider", "", "presentation provider (sqlite or postgresql)")
	flags.BoolVar(&options.json, "json", false, "emit the versioned JSON report")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || options.schema == "" || options.root == "" || options.migrations == "" {
		return migrationPlanOptions{}, false
	}
	flags.Visit(func(value *flag.Flag) {
		options.schemaExplicit = options.schemaExplicit || value.Name == "schema"
		options.rootExplicit = options.rootExplicit || value.Name == "root"
		options.migrationExplicit = options.migrationExplicit || value.Name == "migration"
		options.providerExplicit = options.providerExplicit || value.Name == "provider"
	})
	if options.providerExplicit && options.provider != string(ir.SQLite) && options.provider != string(ir.PostgreSQL) {
		return migrationPlanOptions{}, false
	}
	if options.migrationExplicit && (options.migrationID == "" || options.schemaExplicit || options.rootExplicit) {
		return migrationPlanOptions{}, false
	}
	return options, true
}

func buildMigrationPlanReport(ctx context.Context, directory, moduleDir, migrationRoot string, options migrationPlanOptions) (explain.Report, error) {
	if options.migrationExplicit {
		return buildReviewedMigrationPlan(ctx, moduleDir, migrationRoot, migration.MigrationID(options.migrationID))
	}
	previousModel, err := optionalMigrationHead(moduleDir, migrationRoot, false)
	if err != nil {
		return explain.Report{}, err
	}
	request := pipelineRequest(directory, commonOptions{schemaPattern: options.schema, root: options.root}, modelcodegen.PackageSpec{}, nil)
	request.Compile.PreviousModel = previousModel
	request.ReadOnlyDiagnostics = true
	workspace, err := makeMigrationPlanBuildWorkspace(moduleDir)
	if err != nil {
		return explain.Report{}, err
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_ = workspace.cleanup()
		}
	}()
	request.ProspectiveModfileDir = workspace.directory
	result, buildErr := pipeline.Build(ctx, request)
	cleanupErr := workspace.cleanup()
	if cleanupErr != nil {
		return explain.Report{}, cleanupErr
	}
	cleaned = true
	err = buildErr
	if err != nil {
		return explain.Report{}, err
	}
	providers, err := migrationProviders(result.Providers)
	if err != nil {
		return explain.Report{}, err
	}
	preview, err := workflow.Preview(ctx, workflow.PreviewRequest{
		ModuleDir: moduleDir, Root: migrationRoot, ModelFingerprint: result.ModelFingerprint,
		Model: result.Compilation.Model, PreviousModel: previousModel, Providers: providers,
	})
	if err != nil {
		return explain.Report{}, err
	}
	plans := make([]migration.Plan, len(preview.Providers))
	for index := range preview.Providers {
		plans[index] = preview.Providers[index].Plan
	}
	return explain.BuildProspectiveAll(plans)
}

func buildReviewedMigrationPlan(ctx context.Context, moduleDir, migrationRoot string, migrationID migration.MigrationID) (explain.Report, error) {
	providerIDs, err := workflow.PublishedProviders(moduleDir, migrationRoot)
	if err != nil {
		return explain.Report{}, err
	}
	providers, err := reviewedMigrationProviders(providerIDs)
	if err != nil {
		return explain.Report{}, err
	}
	state, err := workflow.Load(ctx, moduleDir, migrationRoot, providers)
	if err != nil {
		return explain.Report{}, err
	}
	providerIDs = providerIDs[:0]
	for providerID := range state.Histories {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Slice(providerIDs, func(i, j int) bool { return providerIDs[i] < providerIDs[j] })
	inputs := make([]explain.ReviewedInput, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		history := state.Histories[providerID]
		var selected *migration.ManifestEntry
		for index := range history.Manifest.Entries {
			if history.Manifest.Entries[index].ID == migrationID {
				entry := history.Manifest.Entries[index]
				selected = &entry
			}
		}
		if selected == nil {
			pendingID, exists, pendingErr := workflow.PendingBackfillID(moduleDir, migrationRoot)
			if pendingErr != nil {
				return explain.Report{}, pendingErr
			}
			if exists && pendingID == migrationID {
				return explain.Report{}, errors.New("pending migration is not immutable reviewed history")
			}
			return explain.Report{}, errors.New("reviewed migration does not exist")
		}
		inputs = append(inputs, explain.ReviewedInput{Entry: *selected, Files: history.Files})
	}
	if len(inputs) == 0 {
		return explain.Report{}, errors.New("reviewed migration history does not exist")
	}
	return explain.BuildReviewedAll(inputs)
}

func redactMigrationPlanError(moduleDir string, err error) error {
	message := err.Error()
	if moduleDir != "" {
		message = strings.ReplaceAll(message, moduleDir, "<module>")
	}
	message = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^[:space:]"'<>]+`).ReplaceAllString(message, "<redacted>")
	message = regexp.MustCompile(`(?:[A-Za-z]:[\\/]|/)[^[:space:]"'<>]+`).ReplaceAllString(message, "<path>")
	return errors.New(message)
}

func migrationPlanWriteError(writer io.Writer, err error) {
	defer func() { _ = recover() }()
	writeError(writer, err)
}

func writeMigrationPlanBuildFailure(stdout, stderr io.Writer, moduleDir string, options migrationPlanOptions, failure *pipeline.DiagnosticsError) int {
	diagnostics := make([]ir.Diagnostic, len(failure.Diagnostics))
	for index, source := range failure.Diagnostics {
		value := source
		value.Message = sanitizeMigrationPlanDiagnosticString(value.Message, moduleDir, options)
		value.Hint = sanitizeMigrationPlanDiagnosticString(value.Hint, moduleDir, options)
		value.Primary = sanitizeMigrationPlanSpan(value.Primary, moduleDir, options)
		value.Related = append([]ir.DiagnosticLabel(nil), source.Related...)
		for relatedIndex := range value.Related {
			value.Related[relatedIndex].Message = sanitizeMigrationPlanDiagnosticString(value.Related[relatedIndex].Message, moduleDir, options)
			value.Related[relatedIndex].StableID = sanitizeMigrationPlanDiagnosticString(value.Related[relatedIndex].StableID, moduleDir, options)
			value.Related[relatedIndex].Span = sanitizeMigrationPlanSpan(value.Related[relatedIndex].Span, moduleDir, options)
		}
		diagnostics[index] = value
	}
	writer := &migrationPlanExactWriter{writer: stdout}
	if err := encodeJSON(writer, buildDiagnosticsOutput{FormatVersion: buildDiagnosticsOutputFormatVersion, Diagnostics: diagnostics}); err != nil {
		migrationPlanWriteError(stderr, errors.New("migration plan diagnostic output could not be written"))
	}
	return 1
}

func sanitizeMigrationPlanSpan(value ir.SourceSpan, moduleDir string, options migrationPlanOptions) ir.SourceSpan {
	value.ModulePath = sanitizeMigrationPlanDiagnosticString(value.ModulePath, moduleDir, options)
	value.RelativeFile = sanitizeMigrationPlanDiagnosticString(value.RelativeFile, moduleDir, options)
	return value
}

func sanitizeMigrationPlanDiagnosticString(value, moduleDir string, options migrationPlanOptions) string {
	if value == "" {
		return ""
	}
	result := redactMigrationPlanError(moduleDir, errors.New(value)).Error()
	for _, argument := range []struct {
		value    string
		explicit bool
	}{
		{options.schema, options.schemaExplicit},
		{options.root, options.rootExplicit},
	} {
		if !argument.explicit || argument.value == "" {
			continue
		}
		if len(argument.value) < 2 {
			return "migration plan source diagnostic"
		}
		result = strings.ReplaceAll(result, argument.value, "<argument>")
	}
	return result
}

type migrationPlanExactWriter struct{ writer io.Writer }

func (writer *migrationPlanExactWriter) Write(value []byte) (int, error) {
	written, err := writer.writer.Write(value)
	if err == nil && written != len(value) {
		return written, io.ErrShortWrite
	}
	return written, err
}
