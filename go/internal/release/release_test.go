package release

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compatibility"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func TestP8ReleaseTagAndVersionAgreement(t *testing.T) {
	commit := strings.Repeat("a", 40)
	manifest := compatibility.DevelopmentManifest()
	manifest.Release = compatibility.Release{Version: "v1.2.3", Tag: "go/v1.2.3", Commit: commit}
	if err := ValidateAgreement(ModulePath, "go/v1.2.3", commit, manifest); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, module, tag, commit string
		manifest                  compatibility.Manifest
	}{
		{name: "unprefixed nested tag", module: ModulePath, tag: "v1.2.3", commit: commit, manifest: manifest},
		{name: "wrong nested prefix", module: ModulePath, tag: "javascript/v1.2.3", commit: commit, manifest: manifest},
		{name: "module mismatch", module: "example.test/golem", tag: "go/v1.2.3", commit: commit, manifest: manifest},
		{name: "manifest version mismatch", module: ModulePath, tag: "go/v1.2.4", commit: commit, manifest: manifest},
		{name: "commit mismatch", module: ModulePath, tag: "go/v1.2.3", commit: strings.Repeat("b", 40), manifest: manifest},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if err := ValidateAgreement(mutation.module, mutation.tag, mutation.commit, mutation.manifest); err == nil {
				t.Fatal("release mismatch was accepted")
			}
		})
	}

	// A moving branch or merely annotated tag is never a candidate. The exact
	// refs/tags/go/vX.Y.Z object must pass git's signature verification first.
	repository := t.TempDir()
	moduleDir := filepath.Join(repository, "go")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), "module "+ModulePath+"\n\ngo 1.25.0\n")
	runTestCommand(t, repository, "git", "init", "-q")
	runTestCommand(t, repository, "git", "config", "user.email", "p8-release@example.test")
	runTestCommand(t, repository, "git", "config", "user.name", "P8 Release Test")
	runTestCommand(t, repository, "git", "add", "go/go.mod")
	runTestCommand(t, repository, "git", "commit", "-qm", "candidate")
	runTestCommand(t, repository, "git", "tag", "-a", "go/v1.2.3", "-m", "unsigned candidate")
	if _, err := InspectCandidate(context.Background(), moduleDir, "go/v1.2.3"); errorCode(err) != CodeUnsignedTag {
		t.Fatalf("unsigned candidate error=%v", err)
	}
	if _, err := InspectCandidate(context.Background(), moduleDir, "go/v1.2.4"); errorCode(err) != CodeUnsignedTag {
		t.Fatalf("moving branch/nonexistent tag error=%v", err)
	}

	signedRepository, signedModule, allowedSigners := signedCandidateRepository(t)
	allowedBytes, err := os.ReadFile(allowedSigners)
	if err != nil {
		t.Fatal(err)
	}
	inspect := InspectConfig{ModuleDir: signedModule, Tag: "go/v1.2.3", AllowedSignersFile: allowedSigners, AllowedSignersSHA256: digest(allowedBytes)}
	wrongSignerTrust := inspect
	wrongSignerTrust.AllowedSignersSHA256 = strings.Repeat("0", 64)
	if _, err := InspectCandidateWithConfig(context.Background(), wrongSignerTrust); errorCode(err) != CodeUnsignedTag {
		t.Fatalf("untrusted signer inventory error=%v", err)
	}
	candidate, err := InspectCandidateWithConfig(context.Background(), inspect)
	if err != nil {
		t.Fatalf("signed candidate rejected: %v", err)
	}
	target := strings.TrimSpace(runTestCommandOutput(t, signedRepository, "git", "rev-parse", "go/v1.2.3^{commit}"))
	materialized, err := compatibility.Parse(candidate.Manifest, compatibility.Digest(candidate.Manifest))
	if err != nil || candidate.Commit != target || ValidateAgreement(ModulePath, candidate.Tag, target, materialized) != nil {
		t.Fatalf("materialized release manifest mismatch candidate=%+v error=%v", candidate, err)
	}
	templateBytes, err := os.ReadFile(filepath.Join(moduleRoot(t), "compatibility", "manifest.json"))
	if err != nil || candidate.TemplateSHA256 != compatibility.Digest(templateBytes) {
		t.Fatal("candidate did not bind the separately trusted checked template")
	}

	// The same signed tag cannot authorize a dirty or moving-branch checkout.
	writeTestFile(t, filepath.Join(signedRepository, "moving.txt"), "moving branch\n")
	runTestCommand(t, signedRepository, "git", "add", "moving.txt")
	runTestCommand(t, signedRepository, "git", "commit", "-qm", "moving branch")
	if _, err := InspectCandidateWithConfig(context.Background(), inspect); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("moving branch candidate error=%v", err)
	}
	runTestCommand(t, signedRepository, "git", "checkout", "-q", "go/v1.2.3")
	writeTestFile(t, filepath.Join(signedModule, "compatibility", "manifest.json"), "tampered template\n")
	if _, err := InspectCandidateWithConfig(context.Background(), inspect); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("tampered checked template error=%v", err)
	}
	runTestCommand(t, signedRepository, "git", "restore", "go/compatibility/manifest.json")
	if _, err := inspectCandidate(context.Background(), inspect, strings.Repeat("0", 64)); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("wrong template trust root error=%v", err)
	}
	tamperedRelease := candidate
	tamperedRelease.Manifest = append([]byte(nil), candidate.Manifest...)
	tamperedRelease.Manifest[len(tamperedRelease.Manifest)-2] ^= 1
	if validateCandidate(tamperedRelease) == nil {
		t.Fatal("tampered materialized release manifest retained candidate authority")
	}
}

func TestP8CleanConsumerModuleResolutionAndGoInstall(t *testing.T) {
	if testing.Short() {
		t.Fatal("row-22 clean consumer evidence cannot be skipped in short mode")
	}
	version := "v0.0.0"
	proxy := buildLocalModuleProxy(t, moduleRoot(t), version)
	proxyURL := (&url.URL{Scheme: "file", Path: proxy}).String() + ",https://proxy.golang.org"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	temporaryParent := filepath.Join(t.TempDir(), "owned-clean-consumer")
	if err := os.MkdirAll(temporaryParent, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporaryParent)
	if err := VerifyCleanConsumer(ctx, ConsumerConfig{Version: version, Proxy: proxyURL, Hermetic: true}); err != nil {
		var closed *Error
		_ = errors.As(err, &closed)
		t.Fatalf("%v stage=%q", err, closed.stage)
	}
	assertNoCleanConsumerRoots(t, temporaryParent)
}

func TestCleanConsumerOwnedTemporaryRootIsRemovedOnSuccessAndFailure(t *testing.T) {
	tests := []struct {
		name    string
		failure error
	}{
		{name: "success"},
		{name: "failure", failure: atStage(fail(CodeConsumer), "deliberate-test-failure")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var root string
			err := withCleanConsumerRoot(func(ownedRoot string) error {
				root = ownedRoot
				readOnlyDirectory := filepath.Join(root, "modcache", "cache", "download", "example.test", "module", "@v")
				if err := os.MkdirAll(readOnlyDirectory, 0o755); err != nil {
					t.Fatal(err)
				}
				readOnlyFile := filepath.Join(readOnlyDirectory, "v1.0.0.zip")
				if err := os.WriteFile(readOnlyFile, []byte("owned module cache bytes"), 0o444); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(readOnlyDirectory, 0o555); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Dir(readOnlyDirectory), 0o555); err != nil {
					t.Fatal(err)
				}
				return test.failure
			})
			if root == "" {
				t.Fatal("clean-consumer helper did not expose its owned root to the operation")
			}
			if _, statErr := os.Lstat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("owned clean-consumer root survived cleanup: root=%q error=%v", root, statErr)
			}
			if test.failure == nil {
				if err != nil {
					t.Fatalf("successful operation failed during cleanup: %v", err)
				}
			} else if err != test.failure {
				t.Fatalf("failure cleanup changed the closed operation error: got=%v want=%v", err, test.failure)
			}
		})
	}
}

func TestVerifyCleanConsumerRemovesOwnedRootAfterOperationFailure(t *testing.T) {
	temporaryParent := filepath.Join(t.TempDir(), "owned-clean-consumer")
	if err := os.MkdirAll(temporaryParent, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporaryParent)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := VerifyCleanConsumer(ctx, ConsumerConfig{
		Version:  "v0.0.0",
		Proxy:    (&url.URL{Scheme: "file", Path: filepath.Join(temporaryParent, "missing-proxy")}).String(),
		Hermetic: true,
	})
	var closed *Error
	if !errors.As(err, &closed) || closed.Code != CodeConsumer || closed.stage != "list-module" {
		t.Fatalf("unexpected clean-consumer failure: error=%v closed=%+v", err, closed)
	}
	assertNoCleanConsumerRoots(t, temporaryParent)
}

func assertNoCleanConsumerRoots(t *testing.T, parent string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(parent, "golem-clean-consumer-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("owned clean-consumer roots survived cleanup: %v", matches)
	}
}

func TestInstalledCLIVersionDocumentIsStrictAndBindsCandidateABI(t *testing.T) {
	valid, err := json.Marshal(map[string]any{
		"formatVersion": FormatVersion,
		"module":        ModulePath,
		"version":       "v1.2.3",
		"commit":        strings.Repeat("0", 40),
		"generatorABI":  manifest.GeneratorVersion,
		"runtimeABI":    manifest.TemplateABIVersion,
	})
	if err != nil || !validInstalledVersion(valid, "v1.2.3") {
		t.Fatal("exact installed CLI version document was rejected")
	}
	mutations := [][]byte{
		append(append([]byte(nil), valid...), []byte("{}")...),
		bytes.Replace(valid, []byte(`"version":"v1.2.3"`), []byte(`"version":"v1.2.4"`), 1),
		bytes.Replace(valid, []byte(`"commit":"`+strings.Repeat("0", 40)+`"`), []byte(`"commit":"`+strings.Repeat("a", 40)+`"`), 1),
		bytes.Replace(valid, []byte(`"generatorABI":"`+manifest.GeneratorVersion+`"`), []byte(`"generatorABI":"unknown"`), 1),
		bytes.Replace(valid, []byte(`"runtimeABI":"`+manifest.TemplateABIVersion+`"`), []byte(`"runtimeABI":"unknown"`), 1),
		bytes.Replace(valid, []byte(`"module":"`+ModulePath+`"`), []byte(`"module":"example.test/other"`), 1),
		bytes.Replace(valid, []byte(`"formatVersion":1`), []byte(`"formatVersion":1,"unknown":true`), 1),
	}
	for index, mutation := range mutations {
		if validInstalledVersion(mutation, "v1.2.3") {
			t.Fatalf("invalid installed CLI version mutation %d was accepted: %s", index, mutation)
		}
	}
}

func TestP8ReleaseArtifactReproducibility(t *testing.T) {
	if testing.Short() {
		t.Fatal("row-22 reproducibility evidence cannot be skipped in short mode")
	}
	_, releaseModule, allowedSigners := signedFullCandidateRepository(t)
	allowedBytes, err := os.ReadFile(allowedSigners)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := InspectCandidateWithConfig(context.Background(), InspectConfig{ModuleDir: releaseModule, Tag: "go/v1.2.3", AllowedSignersFile: allowedSigners, AllowedSignersSHA256: digest(allowedBytes)})
	if err != nil {
		t.Fatal(err)
	}
	platform := Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if _, err := Build(ctx, BuildConfig{ModuleDir: releaseModule, OutputDir: first, Candidate: candidate, Platforms: []Platform{platform}}); err != nil {
		var closed *Error
		_ = errors.As(err, &closed)
		t.Fatalf("%v stage=%q", err, closed.stage)
	}
	if _, err := Build(ctx, BuildConfig{ModuleDir: releaseModule, OutputDir: second, Candidate: candidate, Platforms: []Platform{platform}}); err != nil {
		t.Fatal(err)
	}
	equal, err := equalDirectories(first, second)
	if err != nil || !equal {
		t.Fatalf("independent release builds differ: equal=%t error=%v", equal, err)
	}
	assertReleaseSchemasAndDigests(t, first, candidate)
	firstFiles, _ := readDirectory(first)
	for name, content := range firstFiles {
		if bytes.Contains(content, []byte(releaseModule)) || bytes.Contains(content, []byte(t.TempDir())) {
			t.Fatalf("artifact %s contains an absolute build path", name)
		}
	}
}

func TestP8ExistingVersionArtifactReplacementRefused(t *testing.T) {
	candidate := testCandidate()
	staged := filepath.Join(t.TempDir(), "staged")
	writeTestFile(t, filepath.Join(staged, "golem.tar.gz"), "immutable artifact")
	writeTestFile(t, filepath.Join(staged, "SHA256SUMS"), "immutable checksum")
	releases := filepath.Join(t.TempDir(), "releases")
	if err := Publish(staged, releases, candidate); err != nil {
		t.Fatal(err)
	}
	if err := Publish(staged, releases, candidate); err != nil {
		t.Fatalf("byte-identical retry was not idempotent: %v", err)
	}
	writeTestFile(t, filepath.Join(staged, "golem.tar.gz"), "different bytes")
	if err := Publish(staged, releases, candidate); errorCode(err) != CodeReplacement {
		t.Fatalf("existing version replacement error=%v", err)
	}
	writeTestFile(t, filepath.Join(staged, "golem.tar.gz"), "immutable artifact")
	writeTestFile(t, filepath.Join(staged, "unexpected"), "new path")
	if err := Publish(staged, releases, candidate); errorCode(err) != CodeReplacement {
		t.Fatalf("existing version path addition error=%v", err)
	}
}

func assertReleaseSchemasAndDigests(t *testing.T, root string, candidate Candidate) {
	t.Helper()
	type independentChecksum struct {
		Algorithm     string `json:"algorithm"`
		ChecksumValue string `json:"checksumValue"`
	}
	type independentPackage struct {
		SPDXID           string                `json:"SPDXID"`
		Name             string                `json:"name"`
		VersionInfo      string                `json:"versionInfo"`
		DownloadLocation string                `json:"downloadLocation"`
		FilesAnalyzed    bool                  `json:"filesAnalyzed"`
		LicenseConcluded string                `json:"licenseConcluded"`
		LicenseDeclared  string                `json:"licenseDeclared"`
		CopyrightText    string                `json:"copyrightText"`
		Checksums        []independentChecksum `json:"checksums"`
	}
	type independentRelationship struct {
		SPDXElementID      string `json:"spdxElementId"`
		RelationshipType   string `json:"relationshipType"`
		RelatedSPDXElement string `json:"relatedSpdxElement"`
	}
	type independentSPDX struct {
		SPDXVersion       string `json:"spdxVersion"`
		DataLicense       string `json:"dataLicense"`
		SPDXID            string `json:"SPDXID"`
		Name              string `json:"name"`
		DocumentNamespace string `json:"documentNamespace"`
		CreationInfo      struct {
			Created            string   `json:"created"`
			Creators           []string `json:"creators"`
			LicenseListVersion string   `json:"licenseListVersion"`
		} `json:"creationInfo"`
		DocumentDescribes []string                  `json:"documentDescribes"`
		Packages          []independentPackage      `json:"packages"`
		Relationships     []independentRelationship `json:"relationships"`
	}
	var source independentSPDX
	decodeStrict(t, filepath.Join(root, "golem_source.spdx.json"), &source)
	if source.SPDXVersion != "SPDX-2.3" || source.DataLicense != "CC0-1.0" || source.SPDXID != "SPDXRef-DOCUMENT" || source.DocumentNamespace == "" || len(source.CreationInfo.Creators) == 0 || len(source.Packages) == 0 {
		t.Fatalf("source SPDX required fields=%+v", source)
	}
	if want := time.Unix(candidate.SourceDateEpoch, 0).UTC().Format(time.RFC3339); source.CreationInfo.Created != want || strings.HasPrefix(source.CreationInfo.Created, "2000-") {
		t.Fatalf("source SPDX creation=%q want truthful tag-commit time %q", source.CreationInfo.Created, want)
	}
	for _, pkg := range source.Packages {
		if pkg.SPDXID == "" || pkg.Name == "" || len(pkg.Checksums) != 1 || pkg.Checksums[0].Algorithm != "SHA256" || len(pkg.Checksums[0].ChecksumValue) != 64 {
			t.Fatalf("invalid source SPDX package=%+v", pkg)
		}
		if pkg.Name == ModulePath && pkg.Checksums[0].ChecksumValue != candidate.SourceTreeSHA256 {
			t.Fatalf("main source SPDX checksum=%s want tree=%s", pkg.Checksums[0].ChecksumValue, candidate.SourceTreeSHA256)
		}
	}
	binarySBOMs, err := filepath.Glob(filepath.Join(root, "golem_*_*.spdx.json"))
	if err != nil || len(binarySBOMs) != 1 {
		t.Fatalf("binary SPDX inventory=%v error=%v", binarySBOMs, err)
	}
	var binary independentSPDX
	decodeStrict(t, binarySBOMs[0], &binary)
	if binary.SPDXVersion != "SPDX-2.3" || binary.DataLicense != "CC0-1.0" || binary.SPDXID != "SPDXRef-DOCUMENT" || len(binary.Packages) != 1 || len(binary.Packages[0].Checksums) != 1 || len(binary.Packages[0].Checksums[0].ChecksumValue) != 64 {
		t.Fatalf("invalid binary SPDX=%+v", binary)
	}
	var statement struct {
		Type          string `json:"_type"`
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
		Predicate struct {
			BuildDefinition struct {
				BuildType          string `json:"buildType"`
				ExternalParameters struct {
					Module    string   `json:"module"`
					Version   string   `json:"version"`
					Tag       string   `json:"tag"`
					Commit    string   `json:"commit"`
					Platforms []string `json:"platforms"`
				} `json:"externalParameters"`
				InternalParameters   map[string]any `json:"internalParameters"`
				ResolvedDependencies []struct {
					URI    string            `json:"uri"`
					Digest map[string]string `json:"digest"`
				} `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
			RunDetails struct {
				Builder struct {
					ID string `json:"id"`
				} `json:"builder"`
				Metadata struct {
					InvocationID string `json:"invocationId"`
				} `json:"metadata"`
			} `json:"runDetails"`
		} `json:"predicate"`
	}
	decodeStrict(t, filepath.Join(root, "provenance.json"), &statement)
	if statement.Type != "https://in-toto.io/Statement/v1" || statement.PredicateType != "https://slsa.dev/provenance/v1" || statement.Predicate.BuildDefinition.BuildType == "" || statement.Predicate.BuildDefinition.ExternalParameters.Module != ModulePath || statement.Predicate.BuildDefinition.ExternalParameters.Tag != candidate.Tag || statement.Predicate.BuildDefinition.ExternalParameters.Commit != candidate.Commit || statement.Predicate.RunDetails.Builder.ID == "" || statement.Predicate.RunDetails.Metadata.InvocationID == "" {
		t.Fatalf("invalid in-toto/SLSA statement=%+v", statement)
	}
	if len(statement.Predicate.BuildDefinition.ResolvedDependencies) != 4 {
		t.Fatalf("provenance dependencies=%+v", statement.Predicate.BuildDefinition.ResolvedDependencies)
	}
	resolved := map[string]map[string]string{}
	for _, dependency := range statement.Predicate.BuildDefinition.ResolvedDependencies {
		resolved[dependency.URI] = dependency.Digest
	}
	if resolved["file:compatibility/manifest.json"]["sha256"] != candidate.TemplateSHA256 || resolved["git+https://github.com/eleven-am/golem.git@"+candidate.Tag+"#subdirectory=go"]["sha256"] != candidate.SourceTreeSHA256 || resolved["urn:golem:release-allowed-signers"]["sha256"] != candidate.SignersSHA256 {
		t.Fatalf("provenance omitted trusted template/source tree: %+v", resolved)
	}
	for _, subject := range statement.Subject {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(subject.Name)))
		if err != nil || subject.Digest["sha256"] != digest(content) {
			t.Fatalf("provenance subject %s digest mismatch", subject.Name)
		}
	}
	checksums, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(checksums)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid checksum line %q", line)
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(parts[1])))
		if err != nil || parts[0] != digest(content) {
			t.Fatalf("checksum mismatch %q", line)
		}
	}
	notes, err := os.ReadFile(filepath.Join(root, "RELEASE_NOTES.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{candidate.Version, candidate.Tag, candidate.Commit, compatibility.Digest(candidate.Manifest), "publicGoAPI", "generatedTemplateABI", "Required actions", "Known boundaries", "cdc.requires-adapter", "mysql.unsupported"} {
		if !bytes.Contains(notes, []byte(required)) {
			t.Fatalf("release notes omitted %q: %s", required, notes)
		}
	}
	if bytes.Contains(notes, []byte("2000-01-01")) || bytes.Contains(notes, []byte(moduleRoot(t))) {
		t.Fatal("release notes contain fabricated time or build path")
	}
}

func decodeStrict(t *testing.T, path string, destination any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode %s: %v", filepath.Base(path), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s trailing content: %v", filepath.Base(path), err)
	}
}

func buildLocalModuleProxy(t *testing.T, source, version string) string {
	t.Helper()
	proxy := t.TempDir()
	escaped, err := module.EscapePath(ModulePath)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(proxy, filepath.FromSlash(escaped), "@v")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join(source, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, version+".mod"), string(goMod))
	info, _ := json.Marshal(map[string]any{"Version": version, "Time": time.Unix(1700000000, 0).UTC()})
	writeTestFile(t, filepath.Join(root, version+".info"), string(info)+"\n")
	archive, err := os.Create(filepath.Join(root, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(archive, module.Version{Path: ModulePath, Version: version}, source); err != nil {
		_ = archive.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "list"), version+"\n")
	return proxy
}

func testCandidate() Candidate {
	commit := strings.Repeat("c", 40)
	manifest := compatibility.DevelopmentManifest()
	manifest.Release = compatibility.Release{Version: "v1.2.3", Tag: "go/v1.2.3", Commit: commit}
	encoded, err := compatibility.Encode(manifest)
	if err != nil {
		panic(err)
	}
	template := compatibility.DevelopmentManifest()
	templateBytes, err := compatibility.Encode(template)
	if err != nil {
		panic(err)
	}
	return sealCandidate(Candidate{Module: ModulePath, Version: "v1.2.3", Tag: "go/v1.2.3", Commit: commit, Manifest: encoded, TemplateSHA256: compatibility.Digest(templateBytes), SourceTreeSHA256: strings.Repeat("d", 64), SignersSHA256: strings.Repeat("e", 64), SourceDateEpoch: 1700000000})
}

func signedCandidateRepository(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	moduleDir := filepath.Join(repository, "go")
	if err := os.MkdirAll(filepath.Join(moduleDir, "compatibility"), 0o755); err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join(moduleRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(filepath.Join(moduleRoot(t), "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), string(goMod))
	writeTestFile(t, filepath.Join(moduleDir, "compatibility", "manifest.json"), string(template))
	allowed := signPreparedRepository(t, base, repository)
	return repository, moduleDir, allowed
}

func signedFullCandidateRepository(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	moduleDir := filepath.Join(repository, "go")
	archivePath := filepath.Join(base, "module.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(archive, module.Version{Path: ModulePath, Version: "v1.2.3"}, moduleRoot(t)); err != nil {
		_ = archive.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	prefix := ModulePath + "@v1.2.3/"
	for _, entry := range reader.File {
		if !strings.HasPrefix(entry.Name, prefix) || strings.HasSuffix(entry.Name, "/") {
			continue
		}
		relative := strings.TrimPrefix(entry.Name, prefix)
		destination := filepath.Join(moduleDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		source, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(source)
		_ = source.Close()
		writeErr := os.WriteFile(destination, content, 0o644)
		if err != nil || writeErr != nil {
			t.Fatalf("extract module file %s: %v", relative, err)
		}
	}
	_ = reader.Close()
	allowed := signPreparedRepository(t, base, repository)
	return repository, moduleDir, allowed
}

func signPreparedRepository(t *testing.T, base, repository string) string {
	t.Helper()
	runTestCommand(t, repository, "git", "init", "-q")
	runTestCommand(t, repository, "git", "config", "user.email", "p8-release@example.test")
	runTestCommand(t, repository, "git", "config", "user.name", "P8 Release Test")
	key := filepath.Join(base, "signing-key")
	runTestCommand(t, repository, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	publicKey, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "allowed-signers")
	keyFields := strings.Fields(string(publicKey))
	if len(keyFields) < 2 {
		t.Fatal("generated SSH public key is malformed")
	}
	writeTestFile(t, allowed, "p8-release@example.test "+keyFields[0]+" "+keyFields[1]+"\n")
	runTestCommand(t, repository, "git", "config", "gpg.format", "ssh")
	runTestCommand(t, repository, "git", "config", "user.signingkey", key)
	runTestCommand(t, repository, "git", "config", "gpg.ssh.allowedSignersFile", allowed)
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "signed candidate")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v1.2.3", "-m", "signed candidate")
	return allowed
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func errorCode(err error) ErrorCode {
	var failure *Error
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runTestCommand(t *testing.T, directory, executable string, arguments ...string) {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(arguments, " "), err, output)
	}
}

func runTestCommandOutput(t *testing.T, directory, executable string, arguments ...string) string {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
