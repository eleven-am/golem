package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/migration/workflow"
	"github.com/eleven-am/golem/go/internal/physical"
	internalpostgresql "github.com/eleven-am/golem/go/internal/provider/postgresql"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	publicprovider "github.com/eleven-am/golem/go/provider"
	providerpostgresql "github.com/eleven-am/golem/go/provider/postgresql"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

const (
	diagnosticsFormatVersion uint16 = 1
	diagnosticsModule               = "github.com/eleven-am/golem/go"
	unknownCommit                   = "0000000000000000000000000000000000000000"
)

// buildVersion and buildCommit are release-linker inputs. Their normalized
// values are deliberately closed below so malformed build metadata cannot
// become a diagnostic disclosure channel.
var (
	buildVersion  string
	buildCommit   string
	readBuildInfo = debug.ReadBuildInfo
)

var (
	releasePattern = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type versionOutput struct {
	FormatVersion uint16 `json:"formatVersion"`
	Module        string `json:"module"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	GeneratorABI  string `json:"generatorABI"`
	RuntimeABI    string `json:"runtimeABI"`
}

func currentVersion() versionOutput {
	version, commit := normalizedBuildProvenance()
	return versionOutput{
		FormatVersion: diagnosticsFormatVersion,
		Module:        diagnosticsModule,
		Version:       version,
		Commit:        commit,
		GeneratorABI:  manifest.GeneratorVersion,
		RuntimeABI:    manifest.TemplateABIVersion,
	}
}

func normalizedBuildProvenance() (string, string) {
	// A release archive may bind both values using -X. Never combine one
	// override with unrelated ambient build information.
	if buildVersion != "" || buildCommit != "" {
		if releasePattern.MatchString(buildVersion) {
			return buildVersion, normalizedDevelopmentCommit(buildCommit)
		}
		return "devel", normalizedDevelopmentCommit(buildCommit)
	}
	information, ok := readBuildInfo()
	if !ok || information == nil || information.Main.Path != diagnosticsModule {
		return "devel", unknownCommit
	}
	version := information.Main.Version
	commit := ""
	for _, setting := range information.Settings {
		if setting.Key == "vcs.revision" {
			commit = setting.Value
		}
	}
	if releasePattern.MatchString(version) {
		return version, normalizedDevelopmentCommit(commit)
	}
	return "devel", normalizedDevelopmentCommit(commit)
}

func normalizedDevelopmentCommit(value string) string {
	if commitPattern.MatchString(value) {
		return value
	}
	return unknownCommit
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		writeError(stderr, errors.New(usage))
		return 2
	}
	value := currentVersion()
	var err error
	if *jsonOutput {
		err = encodeJSON(stdout, value)
	} else {
		_, err = fmt.Fprintf(stdout, "golem %s (commit %s)\n", value.Version, value.Commit)
	}
	if err != nil {
		writeError(stderr, errors.New("diagnostic output failed"))
		return 1
	}
	return 0
}

type doctorDiagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
}

type doctorOutput struct {
	FormatVersion uint16             `json:"formatVersion"`
	Release       string             `json:"release"`
	Provider      string             `json:"provider"`
	Capabilities  string             `json:"capabilities"`
	History       string             `json:"history"`
	Schema        string             `json:"schema"`
	Generation    string             `json:"generation"`
	Diagnostics   []doctorDiagnostic `json:"diagnostics"`
}

func newDoctorOutput(provider string) doctorOutput {
	return doctorOutput{
		FormatVersion: diagnosticsFormatVersion,
		Release:       currentVersion().Version,
		Provider:      provider,
		Capabilities:  "fail",
		History:       "incomplete",
		Schema:        "unreachable",
		Generation:    "incompatible",
		Diagnostics:   []doctorDiagnostic{},
	}
}

func (output *doctorOutput) add(code, severity string) {
	output.Diagnostics = append(output.Diagnostics, doctorDiagnostic{Code: code, Severity: severity})
}

func (output doctorOutput) healthy() bool {
	return output.Capabilities == "pass" && output.History == "current" && output.Schema == "current" && output.Generation == "current"
}

func runDoctor(ctx context.Context, directory string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	schemaPattern := flags.String("schema", ".", "Go package pattern containing the schema root")
	root := flags.String("root", "DefineSchema", "schema root function name")
	migrations := flags.String("migrations", workflow.DefaultRoot, "module-relative migration history root")
	providerName := flags.String("provider", "", "database provider")
	dsn := flags.String("dsn", "", "explicit provider data source name")
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *schemaPattern == "" || *root == "" || *dsn == "" || (*providerName != string(ir.SQLite) && *providerName != string(ir.PostgreSQL)) {
		writeError(stderr, errors.New(usage))
		return 2
	}

	output := newDoctorOutput(*providerName)
	emit := func() int {
		if err := writeDoctorOutput(stdout, output, *jsonOutput); err != nil {
			writeError(stderr, errors.New("diagnostic output failed"))
			return 1
		}
		if output.healthy() {
			return 0
		}
		return 1
	}

	module, err := resolveModule(directory)
	if err != nil {
		output.add("GOLEM_DOCTOR_MODULE_INVALID", "error")
		return emit()
	}
	migrationRoot, err := canonicalMigrationRoot(*migrations)
	if err != nil {
		output.History = "invalid"
		output.add("GOLEM_DOCTOR_HISTORY_INVALID", "error")
		return emit()
	}

	head, headErr := workflow.ReadModelHead(module.directory, migrationRoot)
	if headErr != nil {
		output.History = "invalid"
		output.add("GOLEM_DOCTOR_HISTORY_INVALID", "error")
	}
	previous, previousErr := readPreviousManifest(module.directory, manifest.DefaultPath)
	app := modelcodegen.PackageSpec{}
	if previousErr == nil && previous != nil {
		app, previousErr = doctorApplicationPackage(module, *previous)
	}
	request := pipelineRequest(directory, commonOptions{schemaPattern: *schemaPattern, root: *root}, app, previous)
	request.Compile.PreviousModel = head
	request.ReadOnlyDiagnostics = true
	result, err := pipeline.Build(ctx, request)
	if err != nil {
		output.add("GOLEM_DOCTOR_SCHEMA_COMPILE_FAILED", "error")
		return emit()
	}

	providerID := ir.Provider(*providerName)
	selected, exists := doctorProviderResult(result, providerID)
	if !exists {
		output.add("GOLEM_DOCTOR_PROVIDER_UNDECLARED", "error")
		return emit()
	}

	providers, err := migrationProviders(result.Providers)
	if err != nil {
		output.History = "invalid"
		output.add("GOLEM_DOCTOR_HISTORY_INVALID", "error")
		return emit()
	}
	state, loadErr := workflow.Load(ctx, module.directory, migrationRoot, providers)
	var history workflow.History
	if loadErr != nil {
		output.History = "invalid"
		if headErr == nil {
			output.add("GOLEM_DOCTOR_HISTORY_INVALID", "error")
		}
	} else if state.Publication == nil {
		output.History = "incomplete"
		output.add("GOLEM_DOCTOR_HISTORY_INCOMPLETE", "warning")
	} else if selectedHistory, ok := state.Histories[providerID]; !ok || len(selectedHistory.Manifest.Entries) == 0 {
		output.History = "incomplete"
		output.add("GOLEM_DOCTOR_HISTORY_INCOMPLETE", "warning")
	} else {
		history = selectedHistory
	}

	generationComparable := false
	if loadErr == nil && state.Publication != nil && verifyGenerationHistory(result, state) == nil {
		request.ReviewedMigrations = reviewedPipelineMigrations(state, result.Providers)
		reviewed, reviewedErr := pipeline.Build(ctx, request)
		if reviewedErr == nil {
			if reviewedSelected, reviewedExists := doctorProviderResult(reviewed, providerID); reviewedExists {
				result = reviewed
				selected = reviewedSelected
				generationComparable = true
			}
		}
	}
	if generationComparable && previousErr == nil && verifyGeneratedPublication(module.directory, result.Prospective) == nil {
		output.Generation = "current"
		output.add("GOLEM_DOCTOR_GENERATION_CURRENT", "info")
	} else {
		output.add("GOLEM_DOCTOR_GENERATION_INCOMPATIBLE", "error")
	}

	database, openErr := openDoctorDatabase(ctx, providerID, *dsn)
	if openErr != nil {
		output.add("GOLEM_DOCTOR_PROVIDER_UNREACHABLE", "error")
		return emit()
	}
	output.Capabilities = "pass"
	output.add("GOLEM_DOCTOR_CAPABILITIES_PASS", "info")

	if loadErr == nil && len(history.Manifest.Entries) != 0 {
		ledger, readErr := readDoctorLedger(ctx, providerID, database, selected.Schema.System)
		if readErr != nil {
			output.History = "incomplete"
			output.add("GOLEM_DOCTOR_LEDGER_UNREADABLE", "error")
		} else if verifyLedgerPrefix(history.Manifest, ledger) != nil {
			output.History = "invalid"
			output.add("GOLEM_DOCTOR_LEDGER_INVALID", "error")
		} else if len(ledger) < len(history.Manifest.Entries) {
			output.History = "pending"
			output.add("GOLEM_DOCTOR_HISTORY_PENDING", "warning")
		} else {
			output.History = "current"
			output.add("GOLEM_DOCTOR_HISTORY_CURRENT", "info")
		}
	}

	switch inspectDoctorSchema(ctx, providerID, database, selected) {
	case "unreachable":
		output.Schema = "unreachable"
		output.add("GOLEM_DOCTOR_SCHEMA_UNREACHABLE", "error")
	case "drift":
		output.Schema = "drift"
		output.add("GOLEM_DOCTOR_SCHEMA_DRIFT", "error")
	default:
		output.Schema = "current"
		output.add("GOLEM_DOCTOR_SCHEMA_CURRENT", "info")
	}
	if closeErr := database.Close(); closeErr != nil {
		output.Capabilities = "fail"
		output.add("GOLEM_DOCTOR_PROVIDER_CLOSE_FAILED", "error")
	}
	return emit()
}

func doctorProviderResult(result pipeline.Result, provider ir.Provider) (pipeline.ProviderResult, bool) {
	for _, candidate := range result.Providers {
		if candidate.Provider.Provider == provider {
			return candidate, true
		}
	}
	return pipeline.ProviderResult{}, false
}

// openDoctorDatabase is a variable so the command's lifecycle can be observed
// by black-box package tests without adding a production provider seam. The
// default always opens through the public provider packages.
var openDoctorDatabase = openDoctorDatabasePublic

func openDoctorDatabasePublic(ctx context.Context, provider ir.Provider, dsn string) (*publicprovider.Database, error) {
	switch provider {
	case ir.SQLite:
		existing, err := sqliteExistingDataSourceName(dsn)
		if err != nil {
			return nil, err
		}
		return providersqlite.Open(ctx, providersqlite.Config{DataSourceName: existing})
	case ir.PostgreSQL:
		return providerpostgresql.Open(ctx, providerpostgresql.Config{DataSourceName: dsn})
	default:
		return nil, errors.New("unsupported provider")
	}
}

func sqliteExistingDataSourceName(value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.Contains(value, "#") {
		return "", errors.New("sqlite doctor data source is invalid")
	}
	base, rawQuery, _ := strings.Cut(value, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", errors.New("sqlite doctor data source is invalid")
	}
	var modes []string
	for key, values := range query {
		if strings.EqualFold(strings.TrimSpace(key), "mode") {
			modes = append(modes, values...)
			if key != "mode" {
				query.Del(key)
			}
		}
	}
	if len(modes) > 1 {
		return "", errors.New("sqlite doctor data source is invalid")
	}
	mode := "rw"
	if len(modes) == 1 {
		mode = strings.ToLower(strings.TrimSpace(modes[0]))
		if mode != "ro" && mode != "rw" {
			return "", errors.New("sqlite doctor requires an existing database")
		}
	}
	query.Set("mode", mode)
	if !strings.HasPrefix(strings.ToLower(base), "file:") {
		base = "file:" + base
	}
	return base + "?" + query.Encode(), nil
}

func readDoctorLedger(ctx context.Context, provider ir.Provider, database *publicprovider.Database, system physical.SystemSchema) ([]migration.LedgerEntry, error) {
	pool := database.UnsafeSQLX()
	if pool == nil {
		return nil, errors.New("provider pool unavailable")
	}
	switch provider {
	case ir.SQLite:
		return internalsqlite.New().ReadLedgerForSchema(ctx, pool, system)
	case ir.PostgreSQL:
		return internalpostgresql.New().ReadLedgerForSchema(ctx, pool, system)
	default:
		return nil, errors.New("unsupported provider")
	}
}

func inspectDoctorSchema(ctx context.Context, provider ir.Provider, database *publicprovider.Database, expected pipeline.ProviderResult) string {
	pool := database.UnsafeSQLX()
	if pool == nil {
		return "unreachable"
	}
	var actual physical.PhysicalSchema
	var err error
	switch provider {
	case ir.SQLite:
		actual, err = internalsqlite.New().Introspect(ctx, pool, expected.Schema)
	case ir.PostgreSQL:
		actual, err = internalpostgresql.New().Introspect(ctx, pool, expected.Schema)
	default:
		return "unreachable"
	}
	if err != nil {
		if !doctorCatalogReadable(ctx, provider, pool) {
			return "unreachable"
		}
		return "drift"
	}
	wantPhysical, wantErr := physical.PhysicalFingerprint(expected.Schema)
	gotPhysical, gotErr := physical.PhysicalFingerprint(actual)
	wantSystem, wantSystemErr := physical.SystemFingerprint(expected.Schema.Provider, expected.Schema.System)
	gotSystem, gotSystemErr := physical.SystemFingerprint(actual.Provider, actual.System)
	if wantErr != nil || gotErr != nil || wantSystemErr != nil || gotSystemErr != nil {
		return "unreachable"
	}
	if wantPhysical != gotPhysical || wantSystem != gotSystem {
		return "drift"
	}
	return "current"
}

func doctorCatalogReadable(ctx context.Context, provider ir.Provider, pool interface {
	GetContext(context.Context, any, string, ...any) error
}) bool {
	if ctx.Err() != nil {
		return false
	}
	var marker int
	var err error
	switch provider {
	case ir.SQLite:
		err = pool.GetContext(ctx, &marker, "SELECT count(*) FROM sqlite_schema")
	case ir.PostgreSQL:
		err = pool.GetContext(ctx, &marker, "SELECT count(*) FROM pg_catalog.pg_class")
	default:
		return false
	}
	return err == nil
}

func doctorApplicationPackage(module moduleIdentity, value manifest.Manifest) (modelcodegen.PackageSpec, error) {
	registryDirectory := ""
	for _, entry := range value.Artifacts {
		if entry.Kind != manifest.ArtifactRegistryGo {
			continue
		}
		if registryDirectory != "" {
			return modelcodegen.PackageSpec{}, errors.New("generated publication has multiple application registries")
		}
		registryDirectory = path.Dir(entry.Path)
	}
	if registryDirectory == "" {
		return modelcodegen.PackageSpec{}, errors.New("generated publication has no application registry")
	}
	return resolveApplicationPackage(module.directory, module, filepath.FromSlash(registryDirectory))
}

func verifyGeneratedPublication(moduleDir string, prospective manifest.Result) error {
	manifestPath := filepath.Join(moduleDir, filepath.FromSlash(manifest.DefaultPath))
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		return errors.New("generated publication is missing")
	}
	actual, err := manifest.Parse(encoded)
	if err != nil || !bytes.Equal(encoded, prospective.Bytes) || !reflect.DeepEqual(actual, prospective.Manifest) {
		return errors.New("generated publication is incompatible")
	}
	for _, entry := range actual.Artifacts {
		content, readErr := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(entry.Path)))
		if readErr != nil || manifest.ContentHash(content) != entry.ContentSHA256 {
			return errors.New("generated publication is incompatible")
		}
	}
	return nil
}

func writeDoctorOutput(writer io.Writer, output doctorOutput, jsonOutput bool) error {
	if jsonOutput {
		return encodeJSON(writer, output)
	}
	if _, err := fmt.Fprintf(writer, "release: %s\nprovider: %s\ncapabilities: %s\nhistory: %s\nschema: %s\ngeneration: %s\ndiagnostics:\n", output.Release, output.Provider, output.Capabilities, output.History, output.Schema, output.Generation); err != nil {
		return err
	}
	for _, diagnostic := range output.Diagnostics {
		if _, err := fmt.Fprintf(writer, "  %s %s\n", diagnostic.Code, diagnostic.Severity); err != nil {
			return err
		}
	}
	return nil
}
