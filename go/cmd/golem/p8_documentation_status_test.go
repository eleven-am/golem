package main

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compatibility"
)

var p8MarkdownLink = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)

// TestP8ExampleContainsNoInternalImportOrOrdinaryResolverClone deliberately
// lives in the framework module as well as the nested example module. Release
// and mutation checks run with GOWORK=off and must still prove that the checked
// consumer is replace-free and contains no handwritten ordinary CRUD surface.
func TestP8ExampleContainsNoInternalImportOrOrdinaryResolverClone(t *testing.T) {
	moduleRoot, _ := p8DocumentationRoots(t)
	exampleRoot := filepath.Join(moduleRoot, "examples", "social")
	moduleFile, err := os.Open(filepath.Join(exampleRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(moduleFile)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 0 && fields[0] == "replace" {
			moduleFile.Close()
			t.Fatalf("external-style example contains a replace directive: %s", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		moduleFile.Close()
		t.Fatal(err)
	}
	if err := moduleFile.Close(); err != nil {
		t.Fatal(err)
	}

	ordinary := map[string]bool{
		"FindUnique": true, "FindFirst": true, "FindMany": true, "Count": true,
		"Create": true, "Update": true, "Upsert": true, "Delete": true,
		"UpdateMany": true, "DeleteMany": true, "Aggregate": true,
		"GroupBy": true, "RelationGroupBy": true,
	}
	err = filepath.WalkDir(exampleRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".go" || strings.Contains(filepath.ToSlash(path), "/golemgqlgen/") || strings.HasPrefix(entry.Name(), "zz_golem_") {
			return walkErr
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		for _, imported := range parsed.Imports {
			value, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if strings.Contains(value, "/internal/") {
				t.Errorf("public example imports internal package %q", value)
			}
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && ordinary[function.Name.Name] {
				t.Errorf("handwritten ordinary backend method %s", function.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP8DocumentationStatusAndLinkAudit(t *testing.T) {
	moduleRoot, repositoryRoot := p8DocumentationRoots(t)
	documents := []string{
		filepath.Join(repositoryRoot, "README.md"),
		filepath.Join(repositoryRoot, "RELEASE_NOTES.md"),
		filepath.Join(repositoryRoot, "docs", "golem-go", "README.md"),
		filepath.Join(repositoryRoot, "docs", "golem-go", "BIBLE.md"),
		filepath.Join(repositoryRoot, "docs", "golem-go", "QUICKSTART.md"),
		filepath.Join(repositoryRoot, "docs", "golem-go", "PRODUCTION.md"),
		filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "P8-PLAN.md"),
		filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "P8-EVIDENCE.md"),
		filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "PUBLIC-PRODUCTION-ABI.md"),
		filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "COMPATIBILITY-MANIFEST.md"),
	}
	for _, document := range documents {
		assertP8MarkdownLinksResolve(t, document)
	}

	rootReadme := p8ReadDocument(t, filepath.Join(repositoryRoot, "README.md"))
	p8Readme := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "README.md"))
	plan := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "P8-PLAN.md"))
	evidence := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "P8-EVIDENCE.md"))
	quickstart := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "QUICKSTART.md"))
	production := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "PRODUCTION.md"))
	productionABI := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "PUBLIC-PRODUCTION-ABI.md"))

	p8RequireDocumentText(t, "root README", rootReadme, "released TypeScript/NestJS implementation", "Go implementation is a separate, unreleased module")
	p8RequireDocumentText(t, "Go documentation index", p8Readme, "P3 authorized reads are complete", "P4 authorized mutations", "P5 generated GraphQL is complete", "P6 analytics and scoped reads are complete", "P7 events", "all mandatory implementation and hosted-release gates are currently `PENDING`")
	p8RequireDocumentText(t, "P8 plan", plan, "implementation in progress", "P8 is not complete while any mandatory ledger row is `PENDING`")
	p8RequireDocumentText(t, "quickstart", quickstart, "unreleased P8 working documentation", "There is no released `vX.Y.Z` to install yet")
	p8RequireDocumentText(t, "production guide", production, "unreleased P8 working documentation", "P8 evidence is still pending")
	p8RequireDocumentText(t, "production ABI", productionABI, "P8 implementation is ongoing")
	p8RequireDocumentText(t, "production ABI", productionABI, "tracked `compatibility/manifest.json` is the canonical development template", "does not and cannot embed the hash of the commit that contains it", "signed provenance binds the checked-template digest, published-manifest digest, tag commit")

	ledgerRows := regexp.MustCompile(`(?m)^\|\s*(?:[1-9]|1[0-9]|2[0-4])\s*\|.*\| \*\*PENDING\*\* \|$`).FindAllString(evidence, -1)
	if len(ledgerRows) != 24 {
		t.Fatalf("P8 formal evidence ledger has %d pending completion rows, want 24", len(ledgerRows))
	}
	if strings.Contains(evidence, "| **PASS** |") || strings.Contains(evidence, "| **FAIL** |") {
		t.Fatal("P8 formal evidence ledger overstates a release completion result")
	}
	if moduleRoot == repositoryRoot {
		t.Fatal("Go nested module and repository root were conflated")
	}
}

func TestP8IntentionalBoundaryDisclosureCorpus(t *testing.T) {
	moduleRoot, repositoryRoot := p8DocumentationRoots(t)
	documents := map[string]string{
		"quickstart":    p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "QUICKSTART.md")),
		"production":    p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "PRODUCTION.md")),
		"public ABI":    p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "PUBLIC-PRODUCTION-ABI.md")),
		"release notes": p8ReadDocument(t, filepath.Join(repositoryRoot, "RELEASE_NOTES.md")),
	}
	needles := []string{
		"federation",
		"mysql",
		"automatic production migration",
		"raw sql",
		"multi-process",
		"cdc",
		"external",
	}
	for name, document := range documents {
		lower := p8NormalizedDocument(document)
		for _, needle := range needles {
			if !strings.Contains(lower, needle) {
				t.Errorf("%s omits intentional boundary %q", name, needle)
			}
		}
		for _, falseCapability := range []string{
			"provides federation",
			"supports mysql",
			"automatic production migration is enabled",
			"observes external writes without a conformant cdc adapter",
			"includes a built-in multi-process broker",
		} {
			if strings.Contains(lower, falseCapability) {
				t.Errorf("%s falsely documents unsupported capability %q", name, falseCapability)
			}
		}
	}

	encoded := []byte(p8ReadDocument(t, filepath.Join(moduleRoot, "compatibility", "manifest.json")))
	manifest, err := compatibility.Parse(encoded, compatibility.TrustedManifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(manifest.KnownBoundaries, ",") != "cdc.requires-adapter,mysql.unsupported" {
		t.Fatalf("machine compatibility boundaries = %#v", manifest.KnownBoundaries)
	}
	if strings.Join(manifest.DeploymentProfiles, ",") != "adapted-multi-process,database-backed-single-process,embedded-single-process" {
		t.Fatalf("machine deployment profiles = %#v", manifest.DeploymentProfiles)
	}
}

func TestP8NoCompletedABIStillClaimsUnimplemented(t *testing.T) {
	moduleRoot, repositoryRoot := p8DocumentationRoots(t)
	productionABI := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "p8", "PUBLIC-PRODUCTION-ABI.md"))
	for _, stale := range []string{
		"Until P8-F lands",
		"generated configuration retains the existing P7",
		"P8-F must either adapt",
	} {
		if strings.Contains(productionABI, stale) {
			t.Fatalf("completed observer ABI still claims pending implementation: %q", stale)
		}
	}
	p8RequireDocumentText(t, "production ABI", productionABI, "P8-F subsequently completed the explicit", "Observer observe.Observer", "no longer retains the superseded `EventObserver events.Observer`")

	completed := map[string]string{
		"p3/P3-EVIDENCE.md": "Status: **COMPLETE",
		"p4/P4-EVIDENCE.md": "Status: **COMPLETE",
		"p5/P5-EVIDENCE.md": "Status: **PASS",
		"p6/P6-EVIDENCE.md": "Status: **complete",
		"p7/P7-EVIDENCE.md": "Status: **complete",
	}
	for relative, status := range completed {
		document := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", filepath.FromSlash(relative)))
		if !strings.Contains(document, status) {
			t.Errorf("completed phase %s no longer carries its authoritative completion status", relative)
		}
	}

	generated := p8ReadDocument(t, filepath.Join(moduleRoot, "examples", "social", "social", "zz_golem_registry.gen.go"))
	for claim, symbol := range map[string]string{
		"authorized reads":        "func (client CallerPostClient[P]) FindMany",
		"authorized mutations":    "func (client CallerPostClient[P]) Create",
		"closure transactions":    "func (caller *Caller[P]) Transaction",
		"analytics":               "func (client CallerPostClient[P]) Aggregate",
		"scoped reads":            "func (client CallerPostClient[P]) Scoped",
		"generated subscriptions": "func (client CallerPostClient[P]) Events",
		"publisher":               "func (app *App[P]) RunEventPublisher",
	} {
		if !strings.Contains(generated, symbol) {
			t.Errorf("completed %s claim has no generated social ABI anchor %q", claim, symbol)
		}
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, "examples", "social", "social", "golemgqlgen", "zz_golem_graphql_exec.gen.go")); err != nil {
		t.Fatalf("completed generated GraphQL claim has no executable artifact: %v", err)
	}
}

func TestP8READMEAndReleaseNotesCapabilityAgreement(t *testing.T) {
	moduleRoot, repositoryRoot := p8DocumentationRoots(t)
	readme := p8ReadDocument(t, filepath.Join(repositoryRoot, "README.md"))
	releaseNotes := p8ReadDocument(t, filepath.Join(repositoryRoot, "RELEASE_NOTES.md"))
	quickstart := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "QUICKSTART.md"))
	production := p8ReadDocument(t, filepath.Join(repositoryRoot, "docs", "golem-go", "PRODUCTION.md"))

	p8RequireDocumentText(t, "README", readme, "released TypeScript/NestJS implementation", "Go implementation is a separate, unreleased module", "A TypeScript package version or release note does not imply")
	p8RequireDocumentText(t, "release notes", releaseNotes, "release notes for the TypeScript/NestJS packages", "do not announce a Go module release", "Golem for Go remains unreleased", "observation of external writes without a conformant CDC adapter")
	p8RequireDocumentText(t, "Go quickstart", quickstart, "SQLite", "PostgreSQL", "MySQL", "There is no released `vX.Y.Z`")
	p8RequireDocumentText(t, "Go production guide", production, "Golem supports SQLite and PostgreSQL", "MySQL is not supported", "Writes made outside Golem are invisible unless a conformant CDC adapter is configured")
	for _, falseRelease := range []string{
		"go/v0.6.1",
		"github.com/eleven-am/golem/go/cmd/golem@v0.6.1",
	} {
		if strings.Contains(readme, falseRelease) || strings.Contains(releaseNotes, falseRelease) {
			t.Fatalf("TypeScript release documentation falsely publishes Go version %q", falseRelease)
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(moduleRoot, "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := compatibility.Parse(manifestBytes, compatibility.TrustedManifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Release.Development || manifest.Release.Version != "devel" || manifest.Release.Tag != "" || strings.Trim(manifest.Release.Commit, "0") != "" {
		t.Fatalf("unreleased documentation disagrees with machine release provenance: %#v", manifest.Release)
	}
}

func p8DocumentationRoots(t *testing.T) (moduleRoot, repositoryRoot string) {
	t.Helper()
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot = filepath.Dir(moduleRoot)
	return filepath.Clean(moduleRoot), filepath.Clean(repositoryRoot)
}

func p8ReadDocument(t *testing.T, path string) string {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(encoded)
}

func p8RequireDocumentText(t *testing.T, name, document string, required ...string) {
	t.Helper()
	document = p8NormalizedDocument(document)
	for _, value := range required {
		if !strings.Contains(document, p8NormalizedDocument(value)) {
			t.Errorf("%s omits %q", name, value)
		}
	}
}

func p8NormalizedDocument(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func assertP8MarkdownLinksResolve(t *testing.T, document string) {
	t.Helper()
	lines := strings.Split(p8ReadDocument(t, document), "\n")
	outsideFence := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence {
			outsideFence = append(outsideFence, line)
		}
	}
	encoded := strings.Join(outsideFence, "\n")
	for _, match := range p8MarkdownLink.FindAllStringSubmatch(encoded, -1) {
		target := strings.TrimSpace(match[1])
		if index := strings.IndexAny(target, " \t"); index >= 0 {
			target = target[:index]
		}
		if index := strings.IndexByte(target, '#'); index >= 0 {
			target = target[:index]
		}
		if target == "" || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		if filepath.IsAbs(target) {
			t.Errorf("%s contains a machine-local absolute link %q", document, target)
			continue
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(document), filepath.FromSlash(target)))
		if _, err := os.Stat(resolved); err != nil {
			t.Errorf("%s has unresolved link %q: %v", document, target, err)
		}
	}
}
