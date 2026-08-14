// Package release implements the deterministic, fail-closed P8 release
// candidate builder. It is internal because publishing is a repository concern,
// not an application runtime capability.
package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compatibility"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

const (
	ModulePath    = "github.com/eleven-am/golem/go"
	CLIImportPath = ModulePath + "/cmd/golem"
	FormatVersion = 1
)

var (
	tagPattern             = regexp.MustCompile(`^go/(v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))$`)
	commitPattern          = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	signerPrincipalPattern = regexp.MustCompile(`^[A-Za-z0-9._+@-]+(?:,[A-Za-z0-9._+@-]+)*$`)
)

type ErrorCode string

const (
	CodeInvalidCandidate ErrorCode = "P8_RELEASE_INVALID_CANDIDATE"
	CodeUnsignedTag      ErrorCode = "P8_RELEASE_UNSIGNED_TAG"
	CodeBuild            ErrorCode = "P8_RELEASE_BUILD"
	CodeNonReproducible  ErrorCode = "P8_RELEASE_NON_REPRODUCIBLE"
	CodeReplacement      ErrorCode = "P8_RELEASE_REPLACEMENT_REFUSED"
	CodeConsumer         ErrorCode = "P8_RELEASE_CLEAN_CONSUMER"
)

type Error struct {
	Code  ErrorCode
	stage string
}

func (failure *Error) Error() string {
	if failure == nil || failure.Code == "" {
		return string(CodeInvalidCandidate)
	}
	return string(failure.Code)
}

func fail(code ErrorCode) error { return &Error{Code: code} }

func atStage(err error, stage string) error {
	var closed *Error
	if errors.As(err, &closed) {
		if closed.stage != "" {
			stage += "/" + closed.stage
		}
		return &Error{Code: closed.Code, stage: stage}
	}
	return &Error{Code: CodeBuild, stage: stage}
}

type Candidate struct {
	Module               string
	Version              string
	Tag                  string
	Commit               string
	Manifest             []byte
	TemplateSHA256       string
	SourceTreeSHA256     string
	SignersSHA256        string
	SourceDateEpoch      int64
	migrationGuidePath   string
	migrationGuideSHA256 string
	migrationGuide       []byte
	licenseAuthority     []byte
	projectLicense       []byte
	thirdPartyNotices    []byte
	seal                 string
}

type releaseMigrationGuide struct {
	path   string
	sha256 string
	bytes  []byte
}

type InspectConfig struct {
	ModuleDir            string
	Tag                  string
	AllowedSignersFile   string
	AllowedSignersSHA256 string
}

type Platform struct {
	GOOS   string
	GOARCH string
}

func (platform Platform) ID() string { return platform.GOOS + "_" + platform.GOARCH }

var DefaultPlatforms = []Platform{
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "windows", GOARCH: "amd64"},
	{GOOS: "windows", GOARCH: "arm64"},
}

type BuildConfig struct {
	ModuleDir string
	OutputDir string
	Candidate Candidate
	Platforms []Platform
	GoBinary  string
}

type Inventory struct {
	FormatVersion int      `json:"formatVersion"`
	Module        string   `json:"module"`
	Version       string   `json:"version"`
	Tag           string   `json:"tag"`
	Commit        string   `json:"commit"`
	Files         []Digest `json:"files"`
}

type Digest struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type sourceSBOM struct {
	SPDXVersion       string                 `json:"spdxVersion"`
	DataLicense       string                 `json:"dataLicense"`
	SPDXID            string                 `json:"SPDXID"`
	Name              string                 `json:"name"`
	DocumentNamespace string                 `json:"documentNamespace"`
	CreationInfo      spdxCreation           `json:"creationInfo"`
	DocumentDescribes []string               `json:"documentDescribes"`
	Packages          []spdxPackage          `json:"packages"`
	Relationships     []spdxRelationship     `json:"relationships"`
	ExtractedLicenses []spdxExtractedLicense `json:"hasExtractedLicensingInfos"`
}

type sourceUnit struct {
	Path            string
	Version         string
	Checksum        string
	LicenseDeclared string
}

type archiveEntry struct {
	Name    string
	Mode    fs.FileMode
	Content []byte
}

type binarySBOM struct {
	SPDXVersion       string                 `json:"spdxVersion"`
	DataLicense       string                 `json:"dataLicense"`
	SPDXID            string                 `json:"SPDXID"`
	Name              string                 `json:"name"`
	DocumentNamespace string                 `json:"documentNamespace"`
	CreationInfo      spdxCreation           `json:"creationInfo"`
	DocumentDescribes []string               `json:"documentDescribes"`
	Packages          []spdxPackage          `json:"packages"`
	Relationships     []spdxRelationship     `json:"relationships"`
	ExtractedLicenses []spdxExtractedLicense `json:"hasExtractedLicensingInfos"`
}

type spdxCreation struct {
	Created            string   `json:"created"`
	Creators           []string `json:"creators"`
	LicenseListVersion string   `json:"licenseListVersion"`
}

type spdxChecksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

type spdxPackage struct {
	SPDXID           string         `json:"SPDXID"`
	Name             string         `json:"name"`
	VersionInfo      string         `json:"versionInfo"`
	DownloadLocation string         `json:"downloadLocation"`
	FilesAnalyzed    bool           `json:"filesAnalyzed"`
	LicenseConcluded string         `json:"licenseConcluded"`
	LicenseDeclared  string         `json:"licenseDeclared"`
	CopyrightText    string         `json:"copyrightText"`
	Checksums        []spdxChecksum `json:"checksums"`
}

type spdxRelationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

type spdxExtractedLicense struct {
	LicenseID     string `json:"licenseId"`
	ExtractedText string `json:"extractedText"`
	Name          string `json:"name"`
}

type inTotoStatement struct {
	Type          string          `json:"_type"`
	Subject       []inTotoSubject `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     slsaPredicate   `json:"predicate"`
}

type inTotoSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type slsaPredicate struct {
	BuildDefinition slsaBuildDefinition `json:"buildDefinition"`
	RunDetails      slsaRunDetails      `json:"runDetails"`
}

type slsaBuildDefinition struct {
	BuildType            string                   `json:"buildType"`
	ExternalParameters   slsaExternalParameters   `json:"externalParameters"`
	InternalParameters   map[string]any           `json:"internalParameters"`
	ResolvedDependencies []slsaResolvedDependency `json:"resolvedDependencies"`
}

type slsaExternalParameters struct {
	Module    string   `json:"module"`
	Version   string   `json:"version"`
	Tag       string   `json:"tag"`
	Commit    string   `json:"commit"`
	Platforms []string `json:"platforms"`
}

type slsaResolvedDependency struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

type slsaRunDetails struct {
	Builder  slsaBuilder  `json:"builder"`
	Metadata slsaMetadata `json:"metadata"`
}

type slsaBuilder struct {
	ID string `json:"id"`
}
type slsaMetadata struct {
	InvocationID string `json:"invocationId"`
}

func VersionFromTag(tag string) (string, error) {
	match := tagPattern.FindStringSubmatch(tag)
	if len(match) != 2 {
		return "", fail(CodeInvalidCandidate)
	}
	return match[1], nil
}

// InspectCandidate proves tag/module/manifest agreement and verifies the signed
// annotated tag before any build work begins.
func InspectCandidate(ctx context.Context, moduleDir, tag string) (Candidate, error) {
	return Candidate{}, fail(CodeUnsignedTag)
}

// InspectCandidateWithConfig uses a pinned SSH allowed-signers file as the
// explicit hosted trust channel. Release automation must use this entry point;
// the shorter form exists only for already-configured local Git trust.
func InspectCandidateWithConfig(ctx context.Context, config InspectConfig) (Candidate, error) {
	if config.AllowedSignersFile == "" || len(config.AllowedSignersSHA256) != 64 {
		return Candidate{}, fail(CodeUnsignedTag)
	}
	return inspectCandidate(ctx, config, compatibility.TrustedManifestSHA256)
}

func inspectCandidate(ctx context.Context, config InspectConfig, trustedManifestDigest string) (Candidate, error) {
	moduleDir, tag := config.ModuleDir, config.Tag
	if ctx == nil || moduleDir == "" {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	resolvedModule, err := filepath.EvalSymlinks(moduleDir)
	if err != nil {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	moduleDir = resolvedModule
	version, err := VersionFromTag(tag)
	if err != nil {
		return Candidate{}, err
	}
	repository, err := repositoryRoot(ctx, moduleDir)
	if err != nil {
		return Candidate{}, err
	}
	if config.AllowedSignersFile != "" {
		allowed, resolveErr := filepath.Abs(config.AllowedSignersFile)
		if resolveErr != nil {
			return Candidate{}, fail(CodeUnsignedTag)
		}
		signerDigest, validateErr := validateAllowedSigners(allowed, config.AllowedSignersSHA256)
		if validateErr != nil {
			return Candidate{}, fail(CodeUnsignedTag)
		}
		config.AllowedSignersFile = allowed
		config.AllowedSignersSHA256 = signerDigest
	}
	if err := verifySignedReleaseTag(ctx, repository, tag, config.AllowedSignersFile); err != nil {
		return Candidate{}, fail(CodeUnsignedTag)
	}
	commit, err := output(ctx, repository, nil, "git", "rev-parse", "--verify", "refs/tags/"+tag+"^{commit}")
	if err != nil || !commitPattern.MatchString(commit) {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	epochText, err := output(ctx, repository, nil, "git", "show", "-s", "--format=%ct", commit)
	epoch, parseErr := strconv.ParseInt(epochText, 10, 64)
	if err != nil || parseErr != nil || epoch <= 0 {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	head, err := output(ctx, repository, nil, "git", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != commit {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	status, err := output(ctx, repository, nil, "git", "status", "--porcelain", "--untracked-files=normal")
	if err != nil || status != "" {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	relativeModule, err := filepath.Rel(repository, moduleDir)
	if err != nil || filepath.IsAbs(relativeModule) || strings.HasPrefix(relativeModule, "..") {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	modulePrefix := filepath.ToSlash(relativeModule)
	if modulePrefix == "." {
		modulePrefix = ""
	} else {
		modulePrefix += "/"
	}
	moduleBytes, err := commandOutput(ctx, repository, nil, "git", "show", commit+":"+modulePrefix+"go.mod")
	if err != nil {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	parsed, err := modfile.Parse("go.mod", moduleBytes, nil)
	if err != nil || parsed.Module == nil || parsed.Module.Mod.Path != ModulePath || len(parsed.Replace) != 0 {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	templateBytes, err := commandOutput(ctx, repository, nil, "git", "show", commit+":"+modulePrefix+"compatibility/manifest.json")
	if err != nil {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	template, err := compatibility.Parse(templateBytes, trustedManifestDigest)
	if err != nil || !template.Release.Development || template.Release.Version != "devel" || template.Release.Tag != "" || template.Release.Commit != strings.Repeat("0", 40) {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	released := template
	released.Release = compatibility.Release{Development: false, Version: version, Tag: tag, Commit: commit}
	releaseBytes, err := compatibility.Encode(released)
	if err != nil || ValidateAgreement(ModulePath, tag, commit, released) != nil {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	guide, err := verifyCompatibilityTransition(ctx, repository, modulePrefix, config, released)
	if err != nil {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	licenseAuthority, projectLicense, thirdPartyNotices, err := releaseLicenseEvidenceAt(ctx, repository, modulePrefix, commit, moduleBytes)
	if err != nil {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	sourceTreeSHA256, err := gitModuleTreeDigest(ctx, repository, commit, modulePrefix)
	if err != nil {
		return Candidate{}, fail(CodeInvalidCandidate)
	}
	return sealCandidate(Candidate{Module: ModulePath, Version: version, Tag: tag, Commit: commit, Manifest: releaseBytes, TemplateSHA256: compatibility.Digest(templateBytes), SourceTreeSHA256: sourceTreeSHA256, SignersSHA256: config.AllowedSignersSHA256, SourceDateEpoch: epoch, migrationGuidePath: guide.path, migrationGuideSHA256: guide.sha256, migrationGuide: guide.bytes, licenseAuthority: licenseAuthority, projectLicense: projectLicense, thirdPartyNotices: thirdPartyNotices}), nil
}

func releaseLicenseEvidenceAt(ctx context.Context, repository, modulePrefix, commit string, moduleBytes []byte) ([]byte, []byte, []byte, error) {
	read := func(relative string) ([]byte, error) {
		if !safeReleaseRelativePath(relative) {
			return nil, fail(CodeInvalidCandidate)
		}
		entry, treeErr := output(ctx, repository, nil, "git", "ls-tree", commit, "--", modulePrefix+relative)
		fields := strings.Fields(entry)
		if treeErr != nil || len(fields) != 4 || fields[0] != "100644" || fields[1] != "blob" || fields[3] != modulePrefix+relative {
			return nil, fail(CodeInvalidCandidate)
		}
		return commandOutput(ctx, repository, nil, "git", "show", commit+":"+modulePrefix+relative)
	}
	authorityBytes, err := read(compatibility.DependencyLicenseAuthorityPath)
	if err != nil {
		return nil, nil, nil, fail(CodeInvalidCandidate)
	}
	authority, err := compatibility.ParseDependencyLicenseAuthority(authorityBytes, compatibility.DependencyLicenseAuthoritySHA256)
	if err != nil {
		return nil, nil, nil, fail(CodeInvalidCandidate)
	}
	projectLicense, err := read(authority.Project.Path)
	if err != nil {
		return nil, nil, nil, fail(CodeInvalidCandidate)
	}
	thirdPartyNotices, err := read(authority.Notices.Path)
	if err != nil || compatibility.ValidateDependencyLicenseFiles(authority, projectLicense, thirdPartyNotices) != nil {
		return nil, nil, nil, fail(CodeInvalidCandidate)
	}
	parsed, err := modfile.Parse("go.mod", moduleBytes, nil)
	if err != nil || !dependencyLicenseVersionsMatch(parsed, authority) {
		return nil, nil, nil, fail(CodeInvalidCandidate)
	}
	return authorityBytes, projectLicense, thirdPartyNotices, nil
}

func dependencyLicenseVersionsMatch(moduleFile *modfile.File, authority compatibility.DependencyLicenseAuthority) bool {
	expected := []compatibility.DependencyLicense{
		{Module: "github.com/klauspost/compress", Version: "v1.18.5"},
		{Module: "github.com/nats-io/nats.go", Version: "v1.52.0"},
		{Module: "github.com/nats-io/nkeys", Version: "v0.4.15"},
		{Module: "github.com/nats-io/nuid", Version: "v1.0.1"},
		{Module: "golang.org/x/crypto", Version: "v0.49.0"},
		{Module: "golang.org/x/sys", Version: "v0.47.0"},
	}
	if len(authority.Dependencies) != len(expected) {
		return false
	}
	for index := range expected {
		if authority.Dependencies[index].Module != expected[index].Module || authority.Dependencies[index].Version != expected[index].Version {
			return false
		}
	}
	selected := make(map[string]string, len(moduleFile.Require))
	for _, requirement := range moduleFile.Require {
		selected[requirement.Mod.Path] = requirement.Mod.Version
	}
	for _, dependency := range authority.Dependencies {
		if selected[dependency.Module] != dependency.Version {
			return false
		}
	}
	return true
}

type releaseCompatibilityEvidence struct {
	publicGoAPI    compatibility.APIInventory
	generatedGoABI compatibility.APIInventory
	graphQLABI     compatibility.GraphQLInventory
	observation    compatibility.ObservationInventory
}

func verifySignedReleaseTag(ctx context.Context, repository, tag, allowedSignersFile string) error {
	arguments := []string{"verify-tag", tag}
	if allowedSignersFile != "" {
		arguments = []string{"-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile=" + allowedSignersFile, "verify-tag", tag}
	}
	return run(ctx, repository, nil, "git", arguments...)
}

func verifyCompatibilityTransition(ctx context.Context, repository, modulePrefix string, config InspectConfig, current compatibility.Manifest) (releaseMigrationGuide, error) {
	after, err := compatibilityEvidenceAt(ctx, repository, modulePrefix, current.Release.Commit, current)
	if err != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	previousTag, previousVersion, firstRelease, err := greatestLowerReleaseTag(ctx, repository, current.Release.Tag, current.Release.Version, config.AllowedSignersFile)
	if err != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	if firstRelease {
		if current.MigrationGuide != nil || containsString(current.RequiredActions, "migration-guide.execute") {
			return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
		}
		return releaseMigrationGuide{}, nil
	}
	previousCommit, err := output(ctx, repository, nil, "git", "rev-parse", "--verify", "refs/tags/"+previousTag+"^{commit}")
	if err != nil || !commitPattern.MatchString(previousCommit) {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	if err := run(ctx, repository, nil, "git", "merge-base", "--is-ancestor", previousCommit, current.Release.Commit); err != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	previous, err := developmentManifestAt(ctx, repository, modulePrefix, previousCommit)
	if err != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	previous.Release = compatibility.Release{Development: false, Version: previousVersion, Tag: previousTag, Commit: previousCommit}
	if encoded, encodeErr := compatibility.Encode(previous); encodeErr != nil || ValidateAgreement(ModulePath, previousTag, previousCommit, previous) != nil || len(encoded) == 0 {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	before, err := compatibilityEvidenceAt(ctx, repository, modulePrefix, previousCommit, previous)
	if err != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	guide, err := verifyMigrationGuideTransition(ctx, repository, modulePrefix, previousTag, previousCommit, current, config.AllowedSignersFile)
	if err != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	layers := compatibility.LayerAssessments{
		PublicGoAPI:    compatibility.CompareAPI(before.publicGoAPI, after.publicGoAPI),
		GeneratedGoABI: compatibility.CompareAPI(before.generatedGoABI, after.generatedGoABI),
		GraphQLABI:     compatibility.CompareGraphQL(before.graphQLABI, after.graphQLABI),
		CLIJSON:        conservativeDigestAssessment(previous.Digests.CLIJSON, current.Digests.CLIJSON),
		Observation:    compatibility.CompareObservation(before.observation, after.observation),
	}
	if _, err := compatibility.CompareRelease(previous, current, layers); err != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	return guide, nil
}

func verifyMigrationGuideTransition(ctx context.Context, repository, modulePrefix, previousTag, previousCommit string, current compatibility.Manifest, allowedSignersFile string) (releaseMigrationGuide, error) {
	hasAction := containsString(current.RequiredActions, "migration-guide.execute")
	if current.MigrationGuide == nil {
		if hasAction {
			return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
		}
		return releaseMigrationGuide{}, nil
	}
	if !hasAction {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	authority := *current.MigrationGuide
	encoded, err := commandOutput(ctx, repository, nil, "git", "show", current.Release.Commit+":"+modulePrefix+authority.Path)
	if err != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	guide, err := compatibility.ParseMigrationGuide(encoded, authority.SHA256)
	if err != nil || semver.Compare(authority.ToVersion, current.Release.Version) > 0 {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	sourceTag := authority.FromTag
	sourceCommit, err := output(ctx, repository, nil, "git", "rev-parse", "--verify", "refs/tags/"+sourceTag+"^{commit}")
	if err != nil || !commitPattern.MatchString(sourceCommit) || verifySignedReleaseTag(ctx, repository, sourceTag, allowedSignersFile) != nil || run(ctx, repository, nil, "git", "merge-base", "--is-ancestor", sourceCommit, current.Release.Commit) != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	targetTag := "go/" + authority.ToVersion
	targetCommit := current.Release.Commit
	if authority.ToVersion == current.Release.Version {
		if sourceTag != previousTag || sourceCommit != previousCommit || targetTag != current.Release.Tag {
			return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
		}
	} else {
		if semver.MajorMinor(authority.ToVersion) != semver.MajorMinor(current.Release.Version) {
			return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
		}
		targetCommit, err = output(ctx, repository, nil, "git", "rev-parse", "--verify", "refs/tags/"+targetTag+"^{commit}")
		if err != nil || !commitPattern.MatchString(targetCommit) || verifySignedReleaseTag(ctx, repository, targetTag, allowedSignersFile) != nil ||
			run(ctx, repository, nil, "git", "merge-base", "--is-ancestor", sourceCommit, targetCommit) != nil ||
			run(ctx, repository, nil, "git", "merge-base", "--is-ancestor", targetCommit, current.Release.Commit) != nil {
			return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
		}
		target, targetErr := developmentManifestAt(ctx, repository, modulePrefix, targetCommit)
		if targetErr != nil || target.MigrationGuide == nil || *target.MigrationGuide != authority || !slices.Equal(target.RequiredActions, current.RequiredActions) {
			return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
		}
		targetGuide, targetGuideErr := commandOutput(ctx, repository, nil, "git", "show", targetCommit+":"+modulePrefix+authority.Path)
		if targetGuideErr != nil || !bytes.Equal(targetGuide, encoded) {
			return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
		}
	}
	if compatibility.ValidateMigrationGuideTransition(guide, authority, sourceTag, sourceCommit, targetTag, authority.ToVersion, current.RequiredActions) != nil {
		return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
	}
	for _, corpus := range guide.Corpora {
		commits := []string{sourceCommit, targetCommit, current.Release.Commit}
		for index, commit := range commits {
			if index > 0 && commit == commits[index-1] {
				continue
			}
			object := commit + ":" + modulePrefix + corpus.Path
			actual, resolveErr := output(ctx, repository, nil, "git", "rev-parse", "--verify", object)
			kind, typeErr := output(ctx, repository, nil, "git", "cat-file", "-t", object)
			if resolveErr != nil || typeErr != nil || kind != "tree" || actual != corpus.GitTree {
				return releaseMigrationGuide{}, fail(CodeInvalidCandidate)
			}
		}
	}
	return releaseMigrationGuide{path: authority.Path, sha256: authority.SHA256, bytes: append([]byte(nil), encoded...)}, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func greatestLowerReleaseTag(ctx context.Context, repository, currentTag, currentVersion, allowedSignersFile string) (string, string, bool, error) {
	encoded, err := output(ctx, repository, nil, "git", "for-each-ref", "--format=%(refname:strip=2)", "refs/tags/go/v*")
	if err != nil {
		return "", "", false, fail(CodeInvalidCandidate)
	}
	bestTag, bestVersion := "", ""
	foundCurrent := false
	for _, candidate := range strings.Split(encoded, "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		version, versionErr := VersionFromTag(candidate)
		if versionErr != nil {
			return "", "", false, fail(CodeInvalidCandidate)
		}
		if candidate == currentTag {
			foundCurrent = true
			continue
		}
		if verifySignedReleaseTag(ctx, repository, candidate, allowedSignersFile) != nil || semver.Compare(version, currentVersion) >= 0 {
			return "", "", false, fail(CodeInvalidCandidate)
		}
		if bestVersion == "" || semver.Compare(version, bestVersion) > 0 {
			bestTag, bestVersion = candidate, version
		}
	}
	if !foundCurrent {
		return "", "", false, fail(CodeInvalidCandidate)
	}
	if bestTag == "" {
		return "", "", true, nil
	}
	return bestTag, bestVersion, false, nil
}

func developmentManifestAt(ctx context.Context, repository, modulePrefix, commit string) (compatibility.Manifest, error) {
	encoded, err := commandOutput(ctx, repository, nil, "git", "show", commit+":"+modulePrefix+"compatibility/manifest.json")
	if err != nil {
		return compatibility.Manifest{}, fail(CodeInvalidCandidate)
	}
	value, err := compatibility.ParseHistorical(encoded, compatibility.Digest(encoded))
	if err != nil || !value.Release.Development || value.Release.Version != "devel" || value.Release.Tag != "" || value.Release.Commit != strings.Repeat("0", 40) {
		return compatibility.Manifest{}, fail(CodeInvalidCandidate)
	}
	return value, nil
}

func compatibilityEvidenceAt(ctx context.Context, repository, modulePrefix, commit string, value compatibility.Manifest) (releaseCompatibilityEvidence, error) {
	read := func(path, expected string) ([]byte, error) {
		encoded, err := commandOutput(ctx, repository, nil, "git", "show", commit+":"+modulePrefix+path)
		if err != nil || compatibility.Digest(encoded) != expected {
			return nil, fail(CodeInvalidCandidate)
		}
		return encoded, nil
	}
	publicBytes, err := read("internal/compatibility/testdata/public-go-api.json", value.Digests.PublicGoAPI)
	if err != nil {
		return releaseCompatibilityEvidence{}, err
	}
	public, err := compatibility.ParseAPIInventory(publicBytes)
	if err != nil {
		return releaseCompatibilityEvidence{}, fail(CodeInvalidCandidate)
	}
	generatedBytes, err := read("internal/compatibility/testdata/generated-go-abi.json", value.Digests.GeneratedGoABI)
	if err != nil {
		return releaseCompatibilityEvidence{}, err
	}
	generated, err := compatibility.ParseAPIInventory(generatedBytes)
	if err != nil {
		return releaseCompatibilityEvidence{}, fail(CodeInvalidCandidate)
	}
	graphQLBytes, err := read("internal/compatibility/testdata/graphql-abi.json", value.Digests.GraphQLABI)
	if err != nil {
		return releaseCompatibilityEvidence{}, err
	}
	graphQL, err := compatibility.ParseGraphQLInventory(graphQLBytes)
	if err != nil {
		return releaseCompatibilityEvidence{}, fail(CodeInvalidCandidate)
	}
	observationBytes, err := read("observe/telemetry-manifest.json", value.Digests.Observation)
	if err != nil {
		return releaseCompatibilityEvidence{}, err
	}
	observation, err := compatibility.ParseObservationInventory(observationBytes)
	if err != nil {
		return releaseCompatibilityEvidence{}, fail(CodeInvalidCandidate)
	}
	return releaseCompatibilityEvidence{publicGoAPI: public, generatedGoABI: generated, graphQLABI: graphQL, observation: observation}, nil
}

func conservativeDigestAssessment(previous, current string) compatibility.LayerChange {
	if previous == current {
		return compatibility.LayerUnchanged
	}
	return compatibility.LayerBreaking
}

// ValidateAgreement is the semantic tag/module/version/commit comparison used
// after the tag signature and manifest trust root have independently passed.
func ValidateAgreement(module, tag, commit string, manifest compatibility.Manifest) error {
	version, err := VersionFromTag(tag)
	if err != nil || module != ModulePath || !commitPattern.MatchString(commit) || manifest.Module != module || manifest.Release.Development || manifest.Release.Version != version || manifest.Release.Tag != tag || manifest.Release.Commit != commit {
		return fail(CodeInvalidCandidate)
	}
	return nil
}

func Build(ctx context.Context, config BuildConfig) (Inventory, error) {
	if ctx == nil || validateCandidate(config.Candidate) != nil || config.ModuleDir == "" || config.OutputDir == "" {
		return Inventory{}, fail(CodeInvalidCandidate)
	}
	if err := verifyCandidateSource(ctx, config.ModuleDir, config.Candidate); err != nil {
		return Inventory{}, err
	}
	platforms := append([]Platform(nil), config.Platforms...)
	if len(platforms) == 0 {
		platforms = append(platforms, DefaultPlatforms...)
	}
	if !canonicalPlatforms(platforms) {
		return Inventory{}, fail(CodeInvalidCandidate)
	}
	goBinary := config.GoBinary
	if goBinary == "" {
		goBinary = "go"
	}
	if _, err := os.Stat(config.OutputDir); err == nil || !errors.Is(err, os.ErrNotExist) {
		return Inventory{}, fail(CodeReplacement)
	}
	if err := os.MkdirAll(config.OutputDir, 0o755); err != nil {
		return Inventory{}, fail(CodeBuild)
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(config.OutputDir)
		}
	}()
	modules, err := sourceModules(ctx, config.ModuleDir, goBinary)
	if err != nil {
		return Inventory{}, atStage(err, "source-modules")
	}
	for index := range modules {
		if modules[index].Path == ModulePath {
			modules[index].Checksum = config.Candidate.SourceTreeSHA256
		}
	}
	licenseAuthority, err := compatibility.ParseDependencyLicenseAuthority(config.Candidate.licenseAuthority, compatibility.DependencyLicenseAuthoritySHA256)
	if err != nil || compatibility.ValidateDependencyLicenseFiles(licenseAuthority, config.Candidate.projectLicense, config.Candidate.thirdPartyNotices) != nil || !applySourceModuleLicenses(modules, licenseAuthority) {
		return Inventory{}, fail(CodeInvalidCandidate)
	}
	goVersion, err := output(ctx, config.ModuleDir, cleanGoEnvironment(os.Environ(), false, ""), goBinary, "env", "GOVERSION")
	if err != nil || goVersion == "" {
		return Inventory{}, &Error{Code: CodeBuild, stage: "go-version"}
	}
	source := newSourceSBOM(config.Candidate, modules, licenseAuthority)
	if err := writeCanonicalJSON(filepath.Join(config.OutputDir, "golem_source.spdx.json"), source); err != nil {
		return Inventory{}, err
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, "compatibility-manifest.json"), config.Candidate.Manifest, 0o644); err != nil {
		return Inventory{}, fail(CodeBuild)
	}
	if err := writeCandidateMigrationGuide(config.OutputDir, config.Candidate); err != nil {
		return Inventory{}, err
	}
	notes, err := buildReleaseNotes(config.Candidate)
	if err != nil {
		return Inventory{}, atStage(err, "release-notes")
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, "RELEASE_NOTES.md"), notes, 0o644); err != nil {
		return Inventory{}, fail(CodeBuild)
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, compatibility.ProjectLicensePath), config.Candidate.projectLicense, 0o644); err != nil {
		return Inventory{}, fail(CodeBuild)
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, compatibility.ThirdPartyNoticesPath), config.Candidate.thirdPartyNotices, 0o644); err != nil {
		return Inventory{}, fail(CodeBuild)
	}
	for _, platform := range platforms {
		if err := buildPlatform(ctx, config, goBinary, goVersion, platform); err != nil {
			return Inventory{}, atStage(err, "build-"+platform.ID())
		}
	}
	subjects, err := digestDirectory(config.OutputDir, nil)
	if err != nil {
		return Inventory{}, err
	}
	platformIDs := make([]string, len(platforms))
	for index, platform := range platforms {
		platformIDs[index] = platform.ID()
	}
	statement := newProvenance(config.Candidate, platformIDs, subjects)
	if err := writeCanonicalJSON(filepath.Join(config.OutputDir, "provenance.json"), statement); err != nil {
		return Inventory{}, err
	}
	checksummed, err := digestDirectory(config.OutputDir, map[string]bool{"SHA256SUMS": true, "release-inventory.json": true})
	if err != nil {
		return Inventory{}, err
	}
	var checksum bytes.Buffer
	for _, item := range checksummed {
		fmt.Fprintf(&checksum, "%s  %s\n", item.SHA256, item.Name)
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, "SHA256SUMS"), checksum.Bytes(), 0o644); err != nil {
		return Inventory{}, fail(CodeBuild)
	}
	files, err := digestDirectory(config.OutputDir, map[string]bool{"release-inventory.json": true})
	if err != nil {
		return Inventory{}, err
	}
	inventory := Inventory{FormatVersion: FormatVersion, Module: config.Candidate.Module, Version: config.Candidate.Version, Tag: config.Candidate.Tag, Commit: config.Candidate.Commit, Files: files}
	if err := writeCanonicalJSON(filepath.Join(config.OutputDir, "release-inventory.json"), inventory); err != nil {
		return Inventory{}, err
	}
	failed = false
	return inventory, nil
}

func buildPlatform(ctx context.Context, config BuildConfig, goBinary, goVersion string, platform Platform) error {
	stage, err := os.MkdirTemp("", "golem-release-binary-")
	if err != nil {
		return fail(CodeBuild)
	}
	defer os.RemoveAll(stage)
	binaryName := "golem"
	if platform.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(stage, binaryName)
	environment := cleanBuildEnvironment(os.Environ(), platform)
	linker := "-buildid= -X main.buildVersion=" + config.Candidate.Version + " -X main.buildCommit=" + config.Candidate.Commit
	if err := run(ctx, config.ModuleDir, environment, goBinary, "build", "-trimpath", "-buildvcs=false", "-ldflags", linker, "-o", binary, "./cmd/golem"); err != nil {
		return &Error{Code: CodeBuild, stage: "go-build"}
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		return &Error{Code: CodeBuild, stage: "read-binary"}
	}
	base := "golem_" + strings.TrimPrefix(config.Candidate.Version, "v") + "_" + platform.ID()
	archive := base + ".tar.gz"
	if platform.GOOS == "windows" {
		archive = base + ".zip"
	}
	archivePath := filepath.Join(config.OutputDir, archive)
	archiveTime := time.Unix(config.Candidate.SourceDateEpoch, 0).UTC()
	entries := []archiveEntry{
		{Name: compatibility.ProjectLicensePath, Mode: 0o644, Content: config.Candidate.projectLicense},
		{Name: compatibility.ThirdPartyNoticesPath, Mode: 0o644, Content: config.Candidate.thirdPartyNotices},
		{Name: binaryName, Mode: 0o755, Content: binaryBytes},
	}
	if platform.GOOS == "windows" {
		err = writeZip(archivePath, entries, archiveTime)
	} else {
		err = writeTarGzip(archivePath, entries, archiveTime)
	}
	if err != nil {
		return &Error{Code: CodeBuild, stage: "archive"}
	}
	licenseAuthority, parseErr := compatibility.ParseDependencyLicenseAuthority(config.Candidate.licenseAuthority, compatibility.DependencyLicenseAuthoritySHA256)
	if parseErr != nil {
		return &Error{Code: CodeBuild, stage: "binary-license-authority"}
	}
	sbom := newBinarySBOM(config.Candidate, platform, digest(binaryBytes), goVersion, licenseAuthority.Project.LicenseDeclared)
	if err := writeCanonicalJSON(filepath.Join(config.OutputDir, base+".spdx.json"), sbom); err != nil {
		return &Error{Code: CodeBuild, stage: "binary-sbom"}
	}
	return nil
}

func VerifyReproducible(ctx context.Context, config BuildConfig) error {
	first, err := os.MkdirTemp("", "golem-release-first-")
	if err != nil {
		return fail(CodeBuild)
	}
	defer os.RemoveAll(first)
	second, err := os.MkdirTemp("", "golem-release-second-")
	if err != nil {
		return fail(CodeBuild)
	}
	defer os.RemoveAll(second)
	_ = os.Remove(first)
	_ = os.Remove(second)
	left, right := config, config
	left.OutputDir, right.OutputDir = first, second
	if _, err := Build(ctx, left); err != nil {
		return err
	}
	if _, err := Build(ctx, right); err != nil {
		return err
	}
	equal, err := equalDirectories(first, second)
	if err != nil || !equal {
		return fail(CodeNonReproducible)
	}
	return nil
}

// Publish installs a fully built candidate exactly once. An existing version is
// accepted only when every relative path and byte is identical.
func Publish(staged, releases string, candidate Candidate) error {
	if validateCandidate(candidate) != nil || staged == "" || releases == "" {
		return fail(CodeInvalidCandidate)
	}
	if err := verifyBuiltMigrationGuide(staged, candidate); err != nil {
		return err
	}
	if err := verifyBuiltLicenseEvidence(staged, candidate); err != nil {
		return err
	}
	target := filepath.Join(releases, candidate.Version)
	if _, err := os.Stat(target); err == nil {
		equal, compareErr := equalDirectories(staged, target)
		if compareErr != nil || !equal {
			return fail(CodeReplacement)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(CodeReplacement)
	}
	if err := os.MkdirAll(releases, 0o755); err != nil {
		return fail(CodeBuild)
	}
	if err := copyDirectory(staged, target); err != nil {
		_ = os.RemoveAll(target)
		return fail(CodeBuild)
	}
	return nil
}

type ConsumerConfig struct {
	Version  string
	Proxy    string
	Go       string
	Hermetic bool
}

// VerifyCleanConsumer resolves and installs only through module version syntax
// in fresh GOPATH/GOMODCACHE state. It never creates a go.work or replace.
func VerifyCleanConsumer(ctx context.Context, config ConsumerConfig) error {
	if ctx == nil || VersionFromCandidate(config.Version) != nil || config.Proxy == "" {
		return fail(CodeConsumer)
	}
	goBinary := config.Go
	if goBinary == "" {
		goBinary = "go"
	}
	return withCleanConsumerRoot(func(root string) error {
		return verifyCleanConsumerAtRoot(ctx, config, goBinary, root)
	})
}

func withCleanConsumerRoot(run func(root string) error) (result error) {
	root, err := os.MkdirTemp("", "golem-clean-consumer-")
	if err != nil {
		return fail(CodeConsumer)
	}
	defer func() {
		if cleanupErr := removeOwnedTempTree(root); cleanupErr != nil && result == nil {
			result = atStage(fail(CodeConsumer), "cleanup")
		}
	}()
	return run(root)
}

// removeOwnedTempTree is deliberately limited to a root created by
// withCleanConsumerRoot. The Go command makes module-cache directories and
// files read-only, so a plain os.RemoveAll can leave hundreds of megabytes
// behind. Making only this owned tree writable first keeps cleanup reliable
// without changing permissions in any user or shared module cache.
func removeOwnedTempTree(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Dir(root) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(root), "golem-clean-consumer-") {
		return errors.New("invalid clean-consumer temporary root")
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		}
		return os.Chmod(path, mode)
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("clean-consumer temporary root still exists")
		}
		return err
	}
	return nil
}

func verifyCleanConsumerAtRoot(ctx context.Context, config ConsumerConfig, goBinary, root string) error {
	consumer := filepath.Join(root, "consumer")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		return fail(CodeConsumer)
	}
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte("module p8.release.consumer\n\ngo 1.25.0\n"), 0o644); err != nil {
		return fail(CodeConsumer)
	}
	environment := cleanGoEnvironment(os.Environ(), config.Hermetic, ModulePath)
	environment = append(environment, "GOPATH="+filepath.Join(root, "gopath"), "GOMODCACHE="+filepath.Join(root, "modcache"), "GOBIN="+filepath.Join(root, "bin"), "GOPROXY="+config.Proxy)
	if err := run(ctx, consumer, environment, goBinary, "list", "-m", ModulePath+"@"+config.Version); err != nil {
		return atStage(fail(CodeConsumer), "list-module")
	}
	program := `package main

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type Principal struct{ Subject string }
type Actor struct{ Subject string }

type User struct {
	_ struct{}   ` + "`" + `golem:"model;id=p8.release.User;table=users;graphql=User"` + "`" + `
	ID golem.UUID ` + "`" + `db:"id" golem:"id=p8.release.User.ID;pk"` + "`" + `
	Name string    ` + "`" + `db:"name" golem:"type=varchar(80)"` + "`" + `
}

func (User) GolemModel() golem.ModelSpec[User] {
	return golem.DefineModel(golem.GraphQL[User]())
}

func DefineSchema(schema *golem.Schema) {
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func OpenSQLite(ctx context.Context, dsn string) (*provider.Database, error) {
	return providersqlite.Open(ctx, providersqlite.Config{DataSourceName: dsn})
}

func RuntimeConfig(database *provider.Database) golemruntime.Config[Principal, Actor] {
	return golemruntime.Config[Principal, Actor]{
		Database: database,
		ResolvePrincipal: func(_ context.Context, principal Principal) (Actor, error) {
			return Actor{Subject: principal.Subject}, nil
		},
	}
}

var _ = golemruntime.Open[Principal, Actor]

func main() {}
`
	if err := os.WriteFile(filepath.Join(consumer, "main.go"), []byte(program), 0o644); err != nil {
		return fail(CodeConsumer)
	}
	if err := run(ctx, consumer, environment, goBinary, "get",
		ModulePath+"/golem@"+config.Version,
		ModulePath+"/provider@"+config.Version,
		ModulePath+"/provider/sqlite@"+config.Version,
		ModulePath+"/runtime@"+config.Version,
	); err != nil {
		return atStage(fail(CodeConsumer), "get-library")
	}
	if err := run(ctx, consumer, environment, goBinary, "build", "."); err != nil {
		return atStage(fail(CodeConsumer), "build-library")
	}
	if err := run(ctx, consumer, environment, goBinary, "install", CLIImportPath+"@"+config.Version); err != nil {
		return atStage(fail(CodeConsumer), "install-cli")
	}
	binary := filepath.Join(root, "bin", "golem")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if _, err := os.Stat(binary); err != nil {
		return fail(CodeConsumer)
	}
	encodedVersion, err := commandOutput(ctx, consumer, environment, binary, "version", "--json")
	if err != nil || !validInstalledVersion(encodedVersion, config.Version) {
		return atStage(fail(CodeConsumer), "verify-cli-version")
	}
	return nil
}

func validInstalledVersion(encoded []byte, expected string) bool {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value struct {
		FormatVersion uint16 `json:"formatVersion"`
		Module        string `json:"module"`
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		GeneratorABI  string `json:"generatorABI"`
		RuntimeABI    string `json:"runtimeABI"`
	}
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false
	}
	return value.FormatVersion == FormatVersion &&
		value.Module == ModulePath &&
		value.Version == expected &&
		value.Commit == strings.Repeat("0", 40) &&
		value.GeneratorABI == manifest.GeneratorVersion &&
		value.RuntimeABI == manifest.TemplateABIVersion
}

func cleanGoEnvironment(environment []string, hermetic bool, module string) []string {
	remove := map[string]bool{
		"GOWORK": true, "GOPATH": true, "GOMODCACHE": true, "GOBIN": true, "GOPROXY": true,
		"GOPRIVATE": true, "GONOPROXY": true, "GONOSUMDB": true, "GOSUMDB": true,
	}
	result := make([]string, 0, len(environment)+3)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !remove[name] {
			result = append(result, entry)
		}
	}
	result = append(result, "GOWORK=off")
	if hermetic {
		result = append(result, "GONOSUMDB="+module, "GOSUMDB=off")
	}
	return result
}

func VersionFromCandidate(version string) error {
	if _, err := VersionFromTag("go/" + version); err != nil {
		return err
	}
	return nil
}

func validateCandidate(candidate Candidate) error {
	version, err := VersionFromTag(candidate.Tag)
	if err != nil || candidate.Module != ModulePath || candidate.Version != version || !commitPattern.MatchString(candidate.Commit) || len(candidate.Manifest) == 0 || len(candidate.TemplateSHA256) != 64 || len(candidate.SourceTreeSHA256) != 64 || len(candidate.SignersSHA256) != 64 || candidate.SourceDateEpoch <= 0 || candidate.seal == "" || candidate.seal != candidateSeal(candidate) {
		return fail(CodeInvalidCandidate)
	}
	manifest, err := compatibility.Parse(candidate.Manifest, compatibility.Digest(candidate.Manifest))
	if err != nil || ValidateAgreement(candidate.Module, candidate.Tag, candidate.Commit, manifest) != nil {
		return fail(CodeInvalidCandidate)
	}
	if manifest.MigrationGuide == nil {
		if candidate.migrationGuidePath != "" || candidate.migrationGuideSHA256 != "" || len(candidate.migrationGuide) != 0 {
			return fail(CodeInvalidCandidate)
		}
	} else {
		authority := *manifest.MigrationGuide
		if candidate.migrationGuidePath != authority.Path || candidate.migrationGuideSHA256 != authority.SHA256 || !safeReleaseRelativePath(candidate.migrationGuidePath) {
			return fail(CodeInvalidCandidate)
		}
		if _, err := compatibility.ParseMigrationGuide(candidate.migrationGuide, candidate.migrationGuideSHA256); err != nil {
			return fail(CodeInvalidCandidate)
		}
	}
	authority, err := compatibility.ParseDependencyLicenseAuthority(candidate.licenseAuthority, compatibility.DependencyLicenseAuthoritySHA256)
	if err != nil || compatibility.ValidateDependencyLicenseFiles(authority, candidate.projectLicense, candidate.thirdPartyNotices) != nil {
		return fail(CodeInvalidCandidate)
	}
	return nil
}

func sealCandidate(candidate Candidate) Candidate {
	candidate.Manifest = append([]byte(nil), candidate.Manifest...)
	candidate.migrationGuide = append([]byte(nil), candidate.migrationGuide...)
	candidate.licenseAuthority = append([]byte(nil), candidate.licenseAuthority...)
	candidate.projectLicense = append([]byte(nil), candidate.projectLicense...)
	candidate.thirdPartyNotices = append([]byte(nil), candidate.thirdPartyNotices...)
	candidate.seal = candidateSeal(candidate)
	return candidate
}

func candidateSeal(candidate Candidate) string {
	return digest([]byte(candidate.Module + "\x00" + candidate.Version + "\x00" + candidate.Tag + "\x00" + candidate.Commit + "\x00" + candidate.TemplateSHA256 + "\x00" + candidate.SourceTreeSHA256 + "\x00" + candidate.SignersSHA256 + "\x00" + strconv.FormatInt(candidate.SourceDateEpoch, 10) + "\x00" + string(candidate.Manifest) + "\x00" + candidate.migrationGuidePath + "\x00" + candidate.migrationGuideSHA256 + "\x00" + string(candidate.migrationGuide) + "\x00" + string(candidate.licenseAuthority) + "\x00" + string(candidate.projectLicense) + "\x00" + string(candidate.thirdPartyNotices)))
}

func safeReleaseRelativePath(value string) bool {
	if value == "" || filepath.ToSlash(value) != value || filepath.IsAbs(value) || filepath.Clean(filepath.FromSlash(value)) != filepath.FromSlash(value) {
		return false
	}
	for _, element := range strings.Split(value, "/") {
		if element == "" || element == "." || element == ".." {
			return false
		}
	}
	return true
}

func writeCandidateMigrationGuide(root string, candidate Candidate) error {
	if len(candidate.migrationGuide) == 0 {
		return nil
	}
	if !safeReleaseRelativePath(candidate.migrationGuidePath) || digest(candidate.migrationGuide) != candidate.migrationGuideSHA256 {
		return fail(CodeInvalidCandidate)
	}
	target := filepath.Join(root, filepath.FromSlash(candidate.migrationGuidePath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fail(CodeBuild)
	}
	if err := os.WriteFile(target, candidate.migrationGuide, 0o644); err != nil {
		return fail(CodeBuild)
	}
	return nil
}

func verifyBuiltMigrationGuide(root string, candidate Candidate) error {
	if len(candidate.migrationGuide) == 0 {
		return nil
	}
	if !safeReleaseRelativePath(candidate.migrationGuidePath) {
		return fail(CodeInvalidCandidate)
	}
	target := filepath.Join(root, filepath.FromSlash(candidate.migrationGuidePath))
	if !regularFileBelowRoot(root, candidate.migrationGuidePath) {
		return fail(CodeInvalidCandidate)
	}
	encoded, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(encoded, candidate.migrationGuide) || digest(encoded) != candidate.migrationGuideSHA256 {
		return fail(CodeInvalidCandidate)
	}
	return nil
}

func verifyBuiltLicenseEvidence(root string, candidate Candidate) error {
	for relative, expected := range map[string][]byte{
		compatibility.ProjectLicensePath:    candidate.projectLicense,
		compatibility.ThirdPartyNoticesPath: candidate.thirdPartyNotices,
	} {
		if !regularFileBelowRoot(root, relative) {
			return fail(CodeInvalidCandidate)
		}
		encoded, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil || !bytes.Equal(encoded, expected) {
			return fail(CodeInvalidCandidate)
		}
	}
	return nil
}

func regularFileBelowRoot(root, relative string) bool {
	current := filepath.Clean(root)
	elements := strings.Split(relative, "/")
	for index, element := range elements {
		current = filepath.Join(current, element)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		last := index == len(elements)-1
		if last != info.Mode().IsRegular() || !last && !info.IsDir() {
			return false
		}
	}
	return true
}

func buildReleaseNotes(candidate Candidate) ([]byte, error) {
	manifest, err := compatibility.Parse(candidate.Manifest, compatibility.Digest(candidate.Manifest))
	if err != nil || ValidateAgreement(candidate.Module, candidate.Tag, candidate.Commit, manifest) != nil {
		return nil, fail(CodeInvalidCandidate)
	}
	var notes strings.Builder
	fmt.Fprintf(&notes, "# Golem %s\n\n", candidate.Version)
	fmt.Fprintf(&notes, "- Module: `%s`\n- Tag: `%s`\n- Commit: `%s`\n- Compatibility manifest SHA-256: `%s`\n", candidate.Module, candidate.Tag, candidate.Commit, compatibility.Digest(candidate.Manifest))
	notes.WriteString("\n## Compatibility digests\n\n")
	for _, item := range []struct{ name, value string }{
		{"cliJSON", manifest.Digests.CLIJSON}, {"generatedGoABI", manifest.Digests.GeneratedGoABI}, {"graphQLABI", manifest.Digests.GraphQLABI}, {"observation", manifest.Digests.Observation}, {"publicGoAPI", manifest.Digests.PublicGoAPI},
	} {
		fmt.Fprintf(&notes, "- %s: `%s`\n", item.name, item.value)
	}
	notes.WriteString("\n## Format versions\n\n")
	for _, item := range []struct {
		name  string
		value any
	}{
		{"canonicalIR", manifest.Versions.CanonicalIR}, {"contractIR", manifest.Versions.ContractIR}, {"eventSchema", manifest.Versions.EventSchema}, {"generatedManifest", manifest.Versions.GeneratedManifest}, {"generatedTemplateABI", manifest.Versions.GeneratedTemplateABI}, {"generator", manifest.Versions.Generator}, {"graphQL", manifest.Versions.GraphQL}, {"migrationCanonical", manifest.Versions.MigrationCanonical}, {"migrationLedger", manifest.Versions.MigrationLedger}, {"migrationManifest", manifest.Versions.MigrationManifest}, {"modelIR", manifest.Versions.ModelIR}, {"physicalCanonical", manifest.Versions.PhysicalCanonical}, {"physicalSchema", manifest.Versions.PhysicalSchema}, {"schemaBundle", manifest.Versions.SchemaBundle},
	} {
		fmt.Fprintf(&notes, "- %s: `%v`\n", item.name, item.value)
	}
	writeClosedNotesList(&notes, "Fact codecs", manifest.Versions.FactCodecs)
	writeClosedNotesList(&notes, "Event codecs", manifest.Versions.EventCodecs)
	writeClosedNotesList(&notes, "Principal snapshot codecs", manifest.Versions.PrincipalSnapshotCodecs)
	writeClosedNotesList(&notes, "Required actions", manifest.RequiredActions)
	writeClosedNotesList(&notes, "Known boundaries", manifest.KnownBoundaries)
	return []byte(notes.String()), nil
}

func writeClosedNotesList(notes *strings.Builder, heading string, values []string) {
	fmt.Fprintf(notes, "\n## %s\n\n", heading)
	if len(values) == 0 {
		notes.WriteString("- none\n")
		return
	}
	for _, value := range values {
		fmt.Fprintf(notes, "- `%s`\n", value)
	}
}

func canonicalPlatforms(platforms []Platform) bool {
	seen := map[string]bool{}
	for _, platform := range platforms {
		if platform.GOOS == "" || platform.GOARCH == "" || seen[platform.ID()] {
			return false
		}
		seen[platform.ID()] = true
	}
	return true
}

func sourceModules(ctx context.Context, directory, goBinary string) ([]sourceUnit, error) {
	encoded, err := commandOutput(ctx, directory, append(os.Environ(), "GOWORK=off"), goBinary, "list", "-m", "-json", "all")
	if err != nil {
		return nil, fail(CodeBuild)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var result []sourceUnit
	for {
		var value struct {
			Path, Version, Sum string
			Replace            *json.RawMessage
		}
		if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
			break
		} else if err != nil || value.Path == "" || value.Replace != nil {
			return nil, fail(CodeInvalidCandidate)
		}
		checksum := ""
		if value.Path == ModulePath {
			value.Version = "devel"
			moduleBytes, readErr := os.ReadFile(filepath.Join(directory, "go.mod"))
			if readErr != nil {
				return nil, fail(CodeBuild)
			}
			checksum = digest(moduleBytes)
		} else {
			if value.Sum == "" {
				value.Sum, err = downloadModuleSum(ctx, directory, goBinary, value.Path, value.Version)
				if err != nil {
					return nil, fail(CodeBuild)
				}
			}
			checksum, err = moduleChecksum(value.Sum)
			if err != nil {
				return nil, fail(CodeBuild)
			}
		}
		result = append(result, sourceUnit{Path: value.Path, Version: value.Version, Checksum: checksum})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func applySourceModuleLicenses(modules []sourceUnit, authority compatibility.DependencyLicenseAuthority) bool {
	found := make(map[string]bool, len(authority.Dependencies)+1)
	for index := range modules {
		declared, ok := authority.LicenseFor(modules[index].Path, modules[index].Version)
		if ok {
			modules[index].LicenseDeclared = declared
			found[modules[index].Path] = true
		}
	}
	if !found[authority.Project.Module] {
		return false
	}
	for _, dependency := range authority.Dependencies {
		if !found[dependency.Module] {
			return false
		}
	}
	return true
}

func downloadModuleSum(ctx context.Context, directory, goBinary, path, version string) (string, error) {
	encoded, err := commandOutput(ctx, directory, append(os.Environ(), "GOWORK=off"), goBinary, "mod", "download", "-json", path+"@"+version)
	if err != nil {
		return "", err
	}
	var result struct {
		Sum   string
		Error string
	}
	if err := json.Unmarshal(encoded, &result); err != nil || result.Error != "" || result.Sum == "" {
		return "", fail(CodeBuild)
	}
	return result.Sum, nil
}

func moduleChecksum(sum string) (string, error) {
	if !strings.HasPrefix(sum, "h1:") {
		return "", fail(CodeBuild)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sum, "h1:"))
	if err != nil || len(decoded) != sha256.Size {
		return "", fail(CodeBuild)
	}
	return hex.EncodeToString(decoded), nil
}

func validateAllowedSigners(path, expectedDigest string) (string, error) {
	if !digestPattern.MatchString(expectedDigest) {
		return "", fail(CodeUnsignedTag)
	}
	content, err := os.ReadFile(path)
	if err != nil || len(content) == 0 || len(content) > 64<<10 || content[len(content)-1] != '\n' || bytes.Contains(content, []byte{'\r'}) {
		return "", fail(CodeUnsignedTag)
	}
	actual := digest(content)
	if actual != expectedDigest {
		return "", fail(CodeUnsignedTag)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if !sort.StringsAreSorted(lines) {
		return "", fail(CodeUnsignedTag)
	}
	for index, line := range lines {
		if line == "" || index > 0 && line == lines[index-1] {
			return "", fail(CodeUnsignedTag)
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || !signerPrincipalPattern.MatchString(fields[0]) || fields[1] != "ssh-ed25519" || strings.ContainsAny(fields[0], "*?!") {
			return "", fail(CodeUnsignedTag)
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(fields[2])
		if decodeErr != nil || len(decoded) < 32 {
			return "", fail(CodeUnsignedTag)
		}
	}
	return actual, nil
}

func newSourceSBOM(candidate Candidate, modules []sourceUnit, authority compatibility.DependencyLicenseAuthority) sourceSBOM {
	packages := make([]spdxPackage, len(modules))
	relationships := make([]spdxRelationship, len(modules))
	describes := make([]string, 0, 1)
	for index, module := range modules {
		id := fmt.Sprintf("SPDXRef-Package-%d", index+1)
		declared := module.LicenseDeclared
		if declared == "" {
			declared = "NOASSERTION"
		}
		packages[index] = spdxPackage{SPDXID: id, Name: module.Path, VersionInfo: module.Version, DownloadLocation: "NOASSERTION", FilesAnalyzed: false, LicenseConcluded: "NOASSERTION", LicenseDeclared: declared, CopyrightText: "NOASSERTION", Checksums: []spdxChecksum{{Algorithm: "SHA256", ChecksumValue: module.Checksum}}}
		relationships[index] = spdxRelationship{SPDXElementID: "SPDXRef-DOCUMENT", RelationshipType: "DESCRIBES", RelatedSPDXElement: id}
		if module.Path == ModulePath {
			describes = append(describes, id)
		}
	}
	extracted := []spdxExtractedLicense{{LicenseID: authority.Project.LicenseDeclared, ExtractedText: string(candidate.projectLicense), Name: "Golem GPLv3 text; only/or-later unspecified"}}
	for _, dependency := range authority.Dependencies {
		if !strings.HasPrefix(dependency.LicenseDeclared, "LicenseRef-") {
			continue
		}
		license, ok := compatibility.DependencyLicenseText(candidate.thirdPartyNotices, dependency)
		if !ok {
			continue
		}
		extracted = append(extracted, spdxExtractedLicense{LicenseID: dependency.LicenseDeclared, ExtractedText: string(license), Name: dependency.Module + " composite license text"})
	}
	return sourceSBOM{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT", Name: "golem-source-" + candidate.Version,
		DocumentNamespace: "https://eleven-am.github.io/golem/spdx/" + candidate.Version + "/source",
		CreationInfo:      spdxCreation{Created: time.Unix(candidate.SourceDateEpoch, 0).UTC().Format(time.RFC3339), Creators: []string{"Tool: github.com/eleven-am/golem/go/internal/release@v1"}, LicenseListVersion: "3.25"},
		DocumentDescribes: describes, Packages: packages, Relationships: relationships, ExtractedLicenses: extracted,
	}
}

func newBinarySBOM(candidate Candidate, platform Platform, binarySHA, goVersion, licenseDeclared string) binarySBOM {
	id := "SPDXRef-Package-golem"
	return binarySBOM{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT", Name: "golem-" + candidate.Version + "-" + platform.ID(),
		DocumentNamespace: "https://eleven-am.github.io/golem/spdx/" + candidate.Version + "/" + platform.ID(),
		CreationInfo:      spdxCreation{Created: time.Unix(candidate.SourceDateEpoch, 0).UTC().Format(time.RFC3339), Creators: []string{"Tool: github.com/eleven-am/golem/go/internal/release@v1", "Tool: " + goVersion}, LicenseListVersion: "3.25"},
		DocumentDescribes: []string{id},
		Packages:          []spdxPackage{{SPDXID: id, Name: "golem", VersionInfo: candidate.Version, DownloadLocation: "NOASSERTION", FilesAnalyzed: false, LicenseConcluded: "NOASSERTION", LicenseDeclared: licenseDeclared, CopyrightText: "NOASSERTION", Checksums: []spdxChecksum{{Algorithm: "SHA256", ChecksumValue: binarySHA}}}},
		Relationships:     []spdxRelationship{{SPDXElementID: "SPDXRef-DOCUMENT", RelationshipType: "DESCRIBES", RelatedSPDXElement: id}},
		ExtractedLicenses: []spdxExtractedLicense{{LicenseID: licenseDeclared, ExtractedText: string(candidate.projectLicense), Name: "Golem GPLv3 text; only/or-later unspecified"}},
	}
}

func newProvenance(candidate Candidate, platforms []string, subjects []Digest) inTotoStatement {
	inTotoSubjects := make([]inTotoSubject, len(subjects))
	for index, subject := range subjects {
		inTotoSubjects[index] = inTotoSubject{Name: subject.Name, Digest: map[string]string{"sha256": subject.SHA256}}
	}
	invocation := digest([]byte(candidate.Tag + "\x00" + candidate.Commit + "\x00" + strings.Join(platforms, ",")))
	dependencies := []slsaResolvedDependency{
		{URI: "git+https://github.com/eleven-am/golem.git@" + candidate.Tag, Digest: map[string]string{"gitCommit": candidate.Commit}},
		{URI: "git+https://github.com/eleven-am/golem.git@" + candidate.Tag + "#subdirectory=go", Digest: map[string]string{"sha256": candidate.SourceTreeSHA256}},
		{URI: "file:compatibility/manifest.json", Digest: map[string]string{"sha256": candidate.TemplateSHA256}},
		{URI: "urn:golem:release-allowed-signers", Digest: map[string]string{"sha256": candidate.SignersSHA256}},
		{URI: "file:" + compatibility.DependencyLicenseAuthorityPath, Digest: map[string]string{"sha256": compatibility.DependencyLicenseAuthoritySHA256}},
		{URI: "file:" + compatibility.ProjectLicensePath, Digest: map[string]string{"sha256": digest(candidate.projectLicense)}},
		{URI: "file:" + compatibility.ThirdPartyNoticesPath, Digest: map[string]string{"sha256": digest(candidate.thirdPartyNotices)}},
	}
	if len(candidate.migrationGuide) != 0 {
		dependencies = append(dependencies, slsaResolvedDependency{URI: "file:" + candidate.migrationGuidePath, Digest: map[string]string{"sha256": candidate.migrationGuideSHA256}})
	}
	return inTotoStatement{
		Type: "https://in-toto.io/Statement/v1", Subject: inTotoSubjects, PredicateType: "https://slsa.dev/provenance/v1",
		Predicate: slsaPredicate{
			BuildDefinition: slsaBuildDefinition{
				BuildType:            "https://github.com/eleven-am/golem/go/internal/release@v1",
				ExternalParameters:   slsaExternalParameters{Module: candidate.Module, Version: candidate.Version, Tag: candidate.Tag, Commit: candidate.Commit, Platforms: append([]string(nil), platforms...)},
				InternalParameters:   map[string]any{},
				ResolvedDependencies: dependencies,
			},
			RunDetails: slsaRunDetails{Builder: slsaBuilder{ID: "https://github.com/eleven-am/golem/go/internal/release@v1"}, Metadata: slsaMetadata{InvocationID: "urn:sha256:" + invocation}},
		},
	}
}

func cleanBuildEnvironment(environment []string, platform Platform) []string {
	remove := map[string]bool{"GOOS": true, "GOARCH": true, "CGO_ENABLED": true, "GOWORK": true, "GOFLAGS": true}
	result := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !remove[name] {
			result = append(result, entry)
		}
	}
	return append(result, "GOOS="+platform.GOOS, "GOARCH="+platform.GOARCH, "CGO_ENABLED=0", "GOWORK=off")
}

func writeTarGzip(path string, entries []archiveEntry, timestamp time.Time) error {
	if !canonicalArchiveEntries(entries) {
		return fail(CodeInvalidCandidate)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipWriter, _ := gzip.NewWriterLevel(file, gzip.BestCompression)
	gzipWriter.Header.ModTime, gzipWriter.Header.OS = timestamp, 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.Name, Mode: int64(entry.Mode.Perm()), Size: int64(len(entry.Content)), ModTime: timestamp, Uid: 0, Gid: 0, Format: tar.FormatUSTAR}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tarWriter.Write(entry.Content); err != nil {
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	return gzipWriter.Close()
}

func writeZip(path string, entries []archiveEntry, timestamp time.Time) error {
	if !canonicalArchiveEntries(entries) {
		return fail(CodeInvalidCandidate)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.Name, Method: zip.Deflate}
		header.SetModTime(timestamp)
		header.SetMode(item.Mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := entry.Write(item.Content); err != nil {
			return err
		}
	}
	return writer.Close()
}

func canonicalArchiveEntries(entries []archiveEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for index, entry := range entries {
		if !safeReleaseRelativePath(entry.Name) || len(entry.Content) == 0 || entry.Mode != 0o644 && entry.Mode != 0o755 || index > 0 && entries[index-1].Name >= entry.Name {
			return false
		}
	}
	return true
}

func writeCanonicalJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fail(CodeBuild)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return fail(CodeBuild)
	}
	return nil
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func digestDirectory(root string, excluded map[string]bool) ([]Digest, error) {
	var result []Digest
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || filepath.IsAbs(relative) || strings.Contains(relative, "..") || excluded[filepath.ToSlash(relative)] {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result = append(result, Digest{Name: filepath.ToSlash(relative), SHA256: digest(content)})
		return nil
	})
	if err != nil {
		return nil, fail(CodeBuild)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func equalDirectories(left, right string) (bool, error) {
	leftFiles, err := readDirectory(left)
	if err != nil {
		return false, err
	}
	rightFiles, err := readDirectory(right)
	if err != nil || len(leftFiles) != len(rightFiles) {
		return false, err
	}
	for name, content := range leftFiles {
		if !bytes.Equal(content, rightFiles[name]) {
			return false, nil
		}
	}
	return true, nil
}

func readDirectory(root string) (map[string][]byte, error) {
	result := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)], err = os.ReadFile(path)
		return err
	})
	return result, err
}

func copyDirectory(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, 0o644)
	})
}

func repositoryRoot(ctx context.Context, directory string) (string, error) {
	root, err := output(ctx, directory, nil, "git", "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return "", fail(CodeInvalidCandidate)
	}
	return root, nil
}

func verifyCandidateSource(ctx context.Context, moduleDir string, candidate Candidate) error {
	resolved, err := filepath.EvalSymlinks(moduleDir)
	if err != nil {
		return fail(CodeInvalidCandidate)
	}
	repository, err := repositoryRoot(ctx, resolved)
	if err != nil {
		return err
	}
	head, err := output(ctx, repository, nil, "git", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != candidate.Commit {
		return fail(CodeInvalidCandidate)
	}
	status, err := output(ctx, repository, nil, "git", "status", "--porcelain", "--untracked-files=normal")
	if err != nil || status != "" {
		return fail(CodeInvalidCandidate)
	}
	relative, err := filepath.Rel(repository, resolved)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		return fail(CodeInvalidCandidate)
	}
	prefix := filepath.ToSlash(relative)
	if prefix == "." {
		prefix = ""
	} else {
		prefix += "/"
	}
	tree, err := gitModuleTreeDigest(ctx, repository, candidate.Commit, prefix)
	if err != nil || tree != candidate.SourceTreeSHA256 {
		return fail(CodeInvalidCandidate)
	}
	return nil
}

func gitModuleTreeDigest(ctx context.Context, repository, commit, modulePrefix string) (string, error) {
	arguments := []string{"archive", "--format=tar", commit}
	if modulePrefix != "" {
		arguments = append(arguments, "--", strings.TrimSuffix(modulePrefix, "/"))
	}
	archive, err := commandOutput(ctx, repository, nil, "git", arguments...)
	if err != nil || len(archive) == 0 {
		return "", fail(CodeInvalidCandidate)
	}
	return digest(archive), nil
}

func output(ctx context.Context, directory string, environment []string, executable string, arguments ...string) (string, error) {
	encoded, err := commandOutput(ctx, directory, environment, executable, arguments...)
	return strings.TrimSpace(string(encoded)), err
}

func commandOutput(ctx context.Context, directory string, environment []string, executable string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	if environment != nil {
		command.Env = environment
	}
	return command.CombinedOutput()
}

func run(ctx context.Context, directory string, environment []string, executable string, arguments ...string) error {
	_, err := commandOutput(ctx, directory, environment, executable, arguments...)
	return err
}
