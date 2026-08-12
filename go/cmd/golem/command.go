package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/codegen/bindings"
	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/generate/publication"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/migration/workflow"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"golang.org/x/mod/modfile"
)

const usage = `usage:
  golem version [--json]
  golem doctor --provider <sqlite|postgresql> --dsn <explicit> [--schema <pattern>] [--root <name>] [--migrations <path>] [--json]
  golem inspect  [--schema <pattern>] [--root <name>] [--manifest <path>]
  golem generate --app-out <directory> [--schema <pattern>] [--root <name>] [--manifest <path>] [--migrations <path>]
  golem check    --app-out <directory> [--schema <pattern>] [--root <name>] [--manifest <path>] [--migrations <path>]
  golem migration new --name <slug> [--schema <pattern>] [--root <name>] [--migrations <path>] [--approve <operation-id> ...]
  golem migration plan [--schema <pattern>] [--root <name>] [--migrations <path>] [--migration <id>] [--provider <sqlite|postgresql>] [--json]
  golem migration backfill attach --migration <id> --field <Model.Field> --file <path> [--migrations <path>]
  golem migration apply --provider <sqlite|postgresql> --dsn <explicit> [--migrations <path>]`

const generationOutputFormatVersion uint16 = 1
const buildDiagnosticsOutputFormatVersion uint16 = 1

type commonOptions struct {
	schemaPattern string
	root          string
	manifestPath  string
}

type inspectProvider struct {
	Provider          physical.ProviderManifest `json:"provider"`
	Fingerprint       ir.Fingerprint            `json:"fingerprint"`
	SystemFingerprint ir.Fingerprint            `json:"systemFingerprint"`
	Schema            physical.PhysicalSchema   `json:"schema"`
}

type inspectOutput struct {
	FormatVersion       uint16            `json:"formatVersion"`
	Model               ir.ModelIR        `json:"model"`
	Contract            ir.ContractIR     `json:"contract"`
	Providers           []inspectProvider `json:"providers"`
	ModelFingerprint    ir.Fingerprint    `json:"modelFingerprint"`
	ContractFingerprint ir.Fingerprint    `json:"contractFingerprint"`
	GenerationDigest    string            `json:"generationDigest"`
	Policies            []inspectPolicy   `json:"policies"`
	PolicyOperators     []inspectOperator `json:"policyOperators"`
}

type inspectPolicy struct {
	ModelID     ir.ModelID `json:"modelId"`
	PackagePath string     `json:"packagePath"`
	Receiver    string     `json:"receiver"`
	Method      string     `json:"method"`
}

type inspectOperator struct {
	ID                 uint16   `json:"id"`
	Name               string   `json:"name"`
	DeclaredProviders  []string `json:"declaredProviders"`
	AgreementProviders []string `json:"agreementProviders"`
	Capability         uint16   `json:"capability,omitempty"`
	TwoValued          bool     `json:"twoValued"`
}

type generateOutput struct {
	FormatVersion    uint16   `json:"formatVersion"`
	GenerationDigest string   `json:"generationDigest"`
	Changed          []string `json:"changed"`
	Stale            []string `json:"stale"`
	Checked          bool     `json:"checked,omitempty"`
}

type buildDiagnosticsOutput struct {
	FormatVersion uint16          `json:"formatVersion"`
	Diagnostics   []ir.Diagnostic `json:"diagnostics"`
}

func run(ctx context.Context, directory string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeError(stderr, errors.New(usage))
		return 2
	}
	switch args[0] {
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, directory, args[1:], stdout, stderr)
	case "inspect":
		return runInspect(ctx, directory, args[1:], stdout, stderr)
	case "generate":
		return runGeneration(ctx, directory, args[1:], stdout, stderr, "generate", publication.ModePublish)
	case "check":
		return runGeneration(ctx, directory, args[1:], stdout, stderr, "check", publication.ModeCheck)
	case "migration":
		return runMigration(ctx, directory, args[1:], stdout, stderr)
	default:
		writeError(stderr, errors.New(usage))
		return 2
	}
}

func runInspect(ctx context.Context, directory string, args []string, stdout, stderr io.Writer) int {
	options, ok := parseCommon("inspect", args, stderr)
	if !ok {
		return 2
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
	previous, err := readPreviousManifest(module.directory, options.manifestPath)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	request := pipelineRequest(directory, options, modelcodegen.PackageSpec{}, previous)
	head, err := optionalMigrationHead(module.directory, workflow.DefaultRoot, false)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	request.Compile.PreviousModel = head
	result, err := pipeline.Build(ctx, request)
	if err != nil {
		return writeBuildFailure(stdout, stderr, err)
	}
	providers := make([]inspectProvider, len(result.Providers))
	for index, provider := range result.Providers {
		providers[index] = inspectProvider{Provider: provider.Provider, Fingerprint: provider.Fingerprint, SystemFingerprint: provider.SystemFingerprint, Schema: provider.Schema}
	}
	policies := make([]inspectPolicy, 0)
	for _, entry := range result.Bindings {
		if entry.Kind == bindings.BindingPolicy {
			policies = append(policies, inspectPolicy{ModelID: entry.ModelID, PackagePath: entry.PackagePath, Receiver: entry.Receiver, Method: entry.Method})
		}
	}
	operators := make([]inspectOperator, 0, len(operator.Entries()))
	for _, entry := range operator.Entries() {
		operators = append(operators, inspectOperator{ID: uint16(entry.ID()), Name: entry.Name(), DeclaredProviders: inspectPolicyProviders(entry.DeclaredProviders()), AgreementProviders: inspectPolicyProviders(entry.AgreementProviders()), Capability: uint16(entry.Capability()), TwoValued: entry.SQLIsTwoValued()})
	}
	output := inspectOutput{
		FormatVersion: 2, Model: result.Compilation.Model, Contract: result.Compilation.Contract,
		Providers:        providers,
		ModelFingerprint: result.ModelFingerprint, ContractFingerprint: result.ContractFingerprint,
		GenerationDigest: result.Prospective.GenerationDigest,
		Policies:         policies, PolicyOperators: operators,
	}
	if err := encodeJSON(stdout, output); err != nil {
		writeError(stderr, err)
		return 1
	}
	return 0
}

func inspectPolicyProviders(providers policyir.ProviderSet) []string {
	result := make([]string, 0, 2)
	if providers.Valid() {
		for _, provider := range providers.Providers() {
			switch provider {
			case policyir.ProviderSQLite:
				result = append(result, "sqlite")
			case policyir.ProviderPostgreSQL:
				result = append(result, "postgresql")
			}
		}
	}
	return result
}

func runGeneration(ctx context.Context, directory string, args []string, stdout, stderr io.Writer, command string, mode publication.Mode) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	schemaPattern := flags.String("schema", ".", "Go package pattern containing the schema root")
	root := flags.String("root", "DefineSchema", "schema root function name")
	appOut := flags.String("app-out", "", "generated application package directory")
	manifestPath := flags.String("manifest", manifest.DefaultPath, "module-relative generated manifest path")
	migrations := flags.String("migrations", workflow.DefaultRoot, "module-relative migration history root")
	if err := flags.Parse(args); err != nil || *schemaPattern == "" || *appOut == "" || flags.NArg() != 0 {
		writeError(stderr, errors.New(usage))
		return 2
	}
	migrationsExplicit := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == "migrations" {
			migrationsExplicit = true
		}
	})
	module, err := resolveModule(directory)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	migrationRoot, err := canonicalMigrationRoot(*migrations)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if mode == publication.ModeCheck {
		if err := recoverMigrationHistoryIfPresent(ctx, module.directory, migrationRoot, migrationsExplicit); err != nil {
			writeError(stderr, err)
			return 1
		}
	}
	// Recovery is the documented exception to check's ordinary read-only
	// behavior and must happen before resolving output, reading history, or
	// compiling new output.
	if err := publication.Recover(ctx, module.directory); err != nil {
		writeError(stderr, err)
		return 1
	}
	app, err := resolveApplicationPackage(directory, module, *appOut)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	previous, err := readPreviousManifest(module.directory, *manifestPath)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	options := commonOptions{schemaPattern: *schemaPattern, root: *root, manifestPath: *manifestPath}
	request := pipelineRequest(directory, options, app, previous)
	head, err := optionalMigrationHead(module.directory, migrationRoot, migrationsExplicit)
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	request.Compile.PreviousModel = head
	result, err := pipeline.Build(ctx, request)
	if err != nil {
		return writeBuildFailure(stdout, stderr, err)
	}
	exists, statErr := migrationPublicationExists(module.directory, migrationRoot)
	if statErr != nil {
		writeError(stderr, statErr)
		return 1
	}
	if !exists {
		writeError(stderr, errors.New("generated applications require a reviewed non-empty migration history for every declared provider; create the initial migration before generating"))
		return 1
	}
	providers, providerErr := migrationProviders(result.Providers)
	if providerErr != nil {
		writeError(stderr, providerErr)
		return 1
	}
	state, loadErr := workflow.Load(ctx, module.directory, migrationRoot, providers)
	if loadErr != nil {
		writeError(stderr, loadErr)
		return 1
	}
	if verifyErr := verifyGenerationHistory(result, state); verifyErr != nil {
		writeError(stderr, verifyErr)
		return 1
	}
	request.ReviewedMigrations = reviewedPipelineMigrations(state, result.Providers)
	result, err = pipeline.Build(ctx, request)
	if err != nil {
		return writeBuildFailure(stdout, stderr, err)
	}
	published, err := publication.Apply(ctx, publication.Request{
		ModuleDir: result.ModuleDir, ManifestPath: *manifestPath,
		Prospective: result.Prospective, Mode: mode,
	})
	if err != nil {
		writeError(stderr, err)
		return 1
	}
	if err := encodeJSON(stdout, generateOutput{FormatVersion: generationOutputFormatVersion, GenerationDigest: result.Prospective.GenerationDigest, Changed: published.Changed, Stale: published.Stale, Checked: published.Checked}); err != nil {
		writeError(stderr, err)
		return 1
	}
	return 0
}

func reviewedPipelineMigrations(state workflow.State, providers []pipeline.ProviderResult) []pipeline.ReviewedMigration {
	result := make([]pipeline.ReviewedMigration, 0, len(providers))
	for _, provider := range providers {
		history := state.Histories[provider.Provider.Provider]
		result = append(result, pipeline.ReviewedMigration{Manifest: history.Manifest, Files: history.Files})
	}
	return result
}

func verifyGenerationHistory(result pipeline.Result, state workflow.State) error {
	if state.HeadModel == nil {
		return errors.New("reviewed migration history has no ModelIR head")
	}
	headFingerprint, err := ir.ModelFingerprint(*state.HeadModel)
	if err != nil || headFingerprint != result.ModelFingerprint {
		return errors.New("compiled ModelIR differs from the reviewed migration head; create a migration before generating")
	}
	for _, provider := range result.Providers {
		history, exists := state.Histories[provider.Provider.Provider]
		if !exists || len(history.Manifest.Entries) == 0 {
			return fmt.Errorf("reviewed migration history is missing provider %s head", provider.Provider.Provider)
		}
		head := history.Manifest.Entries[len(history.Manifest.Entries)-1]
		if migration.Digest(provider.Fingerprint) != head.AfterPhysical {
			return fmt.Errorf("provider %s physical schema differs from the reviewed migration head; create a migration before generating", provider.Provider.Provider)
		}
		reviewedSystem, fingerprintErr := physical.SystemFingerprint(head.AfterSnapshot.Provider, head.AfterSnapshot.System)
		if fingerprintErr != nil || ir.Fingerprint(reviewedSystem.String()) != provider.SystemFingerprint {
			return fmt.Errorf("provider %s system schema differs from the reviewed migration head", provider.Provider.Provider)
		}
	}
	return nil
}

func parseCommon(name string, args []string, stderr io.Writer) (commonOptions, bool) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := commonOptions{}
	flags.StringVar(&options.schemaPattern, "schema", ".", "Go package pattern containing the schema root")
	flags.StringVar(&options.root, "root", "DefineSchema", "schema root function name")
	flags.StringVar(&options.manifestPath, "manifest", manifest.DefaultPath, "module-relative generated manifest path")
	if err := flags.Parse(args); err != nil || options.schemaPattern == "" || flags.NArg() != 0 {
		writeError(stderr, errors.New(usage))
		return commonOptions{}, false
	}
	return options, true
}

func pipelineRequest(directory string, options commonOptions, app modelcodegen.PackageSpec, previous *manifest.Manifest) pipeline.Request {
	return pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: options.schemaPattern, Root: options.root},
		AppPackage: app, PreviousManifest: previous,
		Lowerers: []physical.Lowerer{sqlite.New(), postgresql.New()},
	}
}

func writeBuildFailure(stdout, stderr io.Writer, err error) int {
	var diagnostics *pipeline.DiagnosticsError
	if errors.As(err, &diagnostics) {
		if encodeErr := encodeJSON(stdout, buildDiagnosticsOutput{
			FormatVersion: buildDiagnosticsOutputFormatVersion,
			Diagnostics:   diagnostics.Diagnostics,
		}); encodeErr != nil {
			writeError(stderr, encodeErr)
		}
		return 1
	}
	writeError(stderr, err)
	return 1
}

func encodeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type moduleIdentity struct {
	path      string
	directory string
}

func resolveModule(start string) (moduleIdentity, error) {
	directory, err := filepath.Abs(start)
	if err != nil {
		return moduleIdentity{}, err
	}
	for {
		goMod := filepath.Join(directory, "go.mod")
		content, readErr := os.ReadFile(goMod)
		if readErr == nil {
			modulePath := modfile.ModulePath(content)
			if modulePath == "" {
				return moduleIdentity{}, fmt.Errorf("module file %s has no module path", goMod)
			}
			return moduleIdentity{path: modulePath, directory: directory}, nil
		}
		if !os.IsNotExist(readErr) {
			return moduleIdentity{}, readErr
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return moduleIdentity{}, fmt.Errorf("cannot find go.mod from %s", start)
		}
		directory = parent
	}
}

func resolveApplicationPackage(base string, module moduleIdentity, output string) (modelcodegen.PackageSpec, error) {
	directory := output
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(base, directory)
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return modelcodegen.PackageSpec{}, err
	}
	relative, err := filepath.Rel(module.directory, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return modelcodegen.PackageSpec{}, fmt.Errorf("application output %q must be inside module %s", output, module.directory)
	}
	importPath := module.path
	if relative != "." {
		importPath += "/" + filepath.ToSlash(relative)
	}
	packageName, err := packageName(directory)
	if err != nil {
		return modelcodegen.PackageSpec{}, err
	}
	return modelcodegen.PackageSpec{ImportPath: importPath, PackageName: packageName, Directory: directory}, nil
}

func packageName(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		filename := filepath.Join(directory, entry.Name())
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), filename, nil, parser.PackageClauseOnly)
		if parseErr != nil {
			return "", fmt.Errorf("read application package name from %s: %w", filename, parseErr)
		}
		names[parsed.Name.Name] = true
	}
	if len(names) > 1 {
		values := make([]string, 0, len(names))
		for name := range names {
			values = append(values, name)
		}
		sort.Strings(values)
		return "", fmt.Errorf("application output has conflicting package names %v", values)
	}
	for name := range names {
		return name, nil
	}
	name := filepath.Base(directory)
	if !token.IsIdentifier(name) || token.Lookup(name).IsKeyword() {
		return "", fmt.Errorf("application output directory %q does not imply a valid Go package name", directory)
	}
	return name, nil
}

func readPreviousManifest(moduleDir, value string) (*manifest.Manifest, error) {
	canonical, err := canonicalManifestPath(value)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(canonical)))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := manifest.ParseHistorical(content)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func canonicalManifestPath(value string) (string, error) {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, `\`) {
		return "", fmt.Errorf("manifest path %q must be canonical and module-relative", value)
	}
	canonical := path.Clean(value)
	if canonical == "." || canonical == ".." || strings.HasPrefix(canonical, "../") || canonical != value {
		return "", fmt.Errorf("manifest path %q must be canonical and module-relative", value)
	}
	return canonical, nil
}
