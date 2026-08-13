package compatibility

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	historicalManifestV1FormatVersion uint16 = 1
	historicalManifestV1Module               = "github.com/eleven-am/golem/go"
)

// Provenance is the exact checked development manifest released at
// go/v0.0.2. The tag resolves to the pinned commit, and the fixture retains
// the exact bytes from the named path. The decoder below is a reviewed
// adaptation because the released v1 source used the then-current Manifest
// DTO; it is not claimed to be a byte-exact copy of that source file.
const (
	historicalManifestV1SourceTag    = "go/v0.0.2"
	historicalManifestV1SourceCommit = "efadc57d1da9b03e84c8cd746323fee3cc2f72c2"
	historicalManifestV1SourcePath   = "go/compatibility/manifest.json"
	historicalManifestV1SourceSHA256 = "59bd82177890ff594f053ab0cc06f4d1a0b15567d85e673ae6ca563602062c1c"
)

// Historical v1 DTOs are the complete released JSON vocabulary. They do not
// embed or alias current manifest DTOs. In particular, manifestV1History has
// no graphQL member: v1 absence means current-only GraphQL support, while a
// relabelled v2 document fails as an unknown-field smuggling attempt.
type manifestV1 struct {
	FormatVersion      uint16               `json:"formatVersion"`
	Module             string               `json:"module"`
	Release            manifestV1Release    `json:"release"`
	MinimumGoVersion   string               `json:"minimumGoVersion"`
	Providers          []manifestV1Provider `json:"providers"`
	DeploymentProfiles []string             `json:"deploymentProfiles"`
	Digests            manifestV1Digests    `json:"digests"`
	Versions           manifestV1Versions   `json:"versions"`
	HistoricalDecode   manifestV1History    `json:"historicalDecode"`
	RequiredActions    []string             `json:"requiredActions"`
	KnownBoundaries    []string             `json:"knownBoundaries"`
}

type manifestV1Release struct {
	Development bool   `json:"development"`
	Version     string `json:"version"`
	Tag         string `json:"tag"`
	Commit      string `json:"commit"`
}

type manifestV1Provider struct {
	Provider             string   `json:"provider"`
	MinimumVersion       string   `json:"minimumVersion"`
	VerificationProfiles []string `json:"verificationProfiles"`
}

type manifestV1Digests struct {
	PublicGoAPI    string `json:"publicGoAPI"`
	GeneratedGoABI string `json:"generatedGoABI"`
	GraphQLABI     string `json:"graphQLABI"`
	CLIJSON        string `json:"cliJSON"`
	Observation    string `json:"observation"`
}

type manifestV1Versions struct {
	Generator               string   `json:"generator"`
	GeneratedTemplateABI    string   `json:"generatedTemplateABI"`
	SchemaBundle            uint16   `json:"schemaBundle"`
	GeneratedManifest       uint16   `json:"generatedManifest"`
	GraphQL                 uint16   `json:"graphQL"`
	ModelIR                 uint16   `json:"modelIR"`
	ContractIR              uint16   `json:"contractIR"`
	CanonicalIR             uint16   `json:"canonicalIR"`
	PhysicalSchema          uint16   `json:"physicalSchema"`
	PhysicalCanonical       uint16   `json:"physicalCanonical"`
	MigrationManifest       uint16   `json:"migrationManifest"`
	MigrationCanonical      uint16   `json:"migrationCanonical"`
	MigrationLedger         uint16   `json:"migrationLedger"`
	EventSchema             uint16   `json:"eventSchema"`
	FactCodecs              []string `json:"factCodecs"`
	EventCodecs             []string `json:"eventCodecs"`
	PrincipalSnapshotCodecs []string `json:"principalSnapshotCodecs"`
}

type manifestV1History struct {
	SchemaBundles           []uint16 `json:"schemaBundles"`
	GeneratedManifests      []uint16 `json:"generatedManifests"`
	ModelIR                 []uint16 `json:"modelIR"`
	ContractIR              []uint16 `json:"contractIR"`
	CanonicalIR             []uint16 `json:"canonicalIR"`
	PhysicalSchema          []uint16 `json:"physicalSchema"`
	PhysicalCanonical       []uint16 `json:"physicalCanonical"`
	MigrationManifest       []uint16 `json:"migrationManifest"`
	MigrationCanonical      []uint16 `json:"migrationCanonical"`
	MigrationLedger         []uint16 `json:"migrationLedger"`
	FactCodecs              []string `json:"factCodecs"`
	EventCodecs             []string `json:"eventCodecs"`
	PrincipalSnapshotCodecs []string `json:"principalSnapshotCodecs"`
}

var (
	manifestV1CommitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	manifestV1GoVersion      = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*))?$`)
	manifestV1ServerVersion  = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*))?$`)
	manifestV1ClosedIdentity = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/@+-]{0,127}$`)
	manifestV1ZeroCommit     = strings.Repeat("0", 40)
)

// ParseHistorical validates a separately trusted current or released
// compatibility manifest. Current v2 bytes use the active exact parser. The
// sole v1 container uses the frozen decoder above and projects its absent
// GraphQL history as the released current-only identity.
func ParseHistorical(encoded []byte, expectedDigest string) (Manifest, error) {
	if !validDigest(expectedDigest) || digest(encoded) != expectedDigest {
		return Manifest{}, fail(ReasonUntrustedDigest)
	}
	var envelope struct {
		FormatVersion uint16 `json:"formatVersion"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&envelope); err != nil {
		return Manifest{}, fail(ReasonInvalidEncoding)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fail(ReasonInvalidEncoding)
	}
	switch envelope.FormatVersion {
	case FormatVersion:
		return parseCurrentManifest(encoded)
	case historicalManifestV1FormatVersion:
		return parseHistoricalManifestV1(encoded)
	default:
		return Manifest{}, fail(ReasonUnsupportedFormat)
	}
}

func parseHistoricalManifestV1(encoded []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value manifestV1
	if err := decoder.Decode(&value); err != nil {
		return Manifest{}, fail(ReasonInvalidEncoding)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fail(ReasonInvalidEncoding)
	}
	if value.FormatVersion != historicalManifestV1FormatVersion {
		return Manifest{}, fail(ReasonUnsupportedFormat)
	}
	if !validHistoricalManifestV1(value) {
		return Manifest{}, fail(ReasonInvalidManifest)
	}
	canonical, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return Manifest{}, fail(ReasonInvalidEncoding)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(canonical, encoded) {
		return Manifest{}, fail(ReasonNoncanonical)
	}
	return projectHistoricalManifestV1(value), nil
}

func validHistoricalManifestV1(value manifestV1) bool {
	if value.Module != historicalManifestV1Module || !validManifestV1Release(value.Release) || !manifestV1GoVersion.MatchString(value.MinimumGoVersion) || len(value.Providers) != 2 || value.DeploymentProfiles == nil || value.RequiredActions == nil || value.KnownBoundaries == nil {
		return false
	}
	for index, provider := range value.Providers {
		if provider.VerificationProfiles == nil || (provider.Provider != "postgresql" && provider.Provider != "sqlite") || !manifestV1ServerVersion.MatchString(provider.MinimumVersion) || !canonicalManifestV1Identities(provider.VerificationProfiles, false) || index > 0 && value.Providers[index-1].Provider >= provider.Provider {
			return false
		}
	}
	if !equalManifestV1Strings(value.DeploymentProfiles, []string{"adapted-multi-process", "database-backed-single-process", "embedded-single-process"}) ||
		!equalManifestV1Strings(value.Providers[0].VerificationProfiles, []string{"c", "linguistic"}) ||
		!equalManifestV1Strings(value.Providers[1].VerificationProfiles, []string{"file", "named-shared-memory"}) {
		return false
	}
	for _, value := range []string{value.Digests.PublicGoAPI, value.Digests.GeneratedGoABI, value.Digests.GraphQLABI, value.Digests.CLIJSON, value.Digests.Observation} {
		if !validManifestV1Digest(value) {
			return false
		}
	}
	versions := value.Versions
	if versions.FactCodecs == nil || versions.EventCodecs == nil || versions.PrincipalSnapshotCodecs == nil || !manifestV1ClosedIdentity.MatchString(versions.Generator) || !manifestV1ClosedIdentity.MatchString(versions.GeneratedTemplateABI) ||
		versions.SchemaBundle == 0 || versions.GeneratedManifest == 0 || versions.GraphQL == 0 || versions.ModelIR == 0 || versions.ContractIR == 0 || versions.CanonicalIR == 0 || versions.PhysicalSchema == 0 || versions.PhysicalCanonical == 0 || versions.MigrationManifest == 0 || versions.MigrationCanonical == 0 || versions.MigrationLedger == 0 || versions.EventSchema == 0 ||
		!canonicalManifestV1Identities(versions.FactCodecs, false) || !canonicalManifestV1Identities(versions.EventCodecs, false) || !canonicalManifestV1Identities(versions.PrincipalSnapshotCodecs, true) {
		return false
	}
	history := value.HistoricalDecode
	if !manifestV1HistoryPresent(history) || !canonicalManifestV1Versions(history.SchemaBundles) || !canonicalManifestV1Versions(history.GeneratedManifests) || !canonicalManifestV1Versions(history.ModelIR) || !canonicalManifestV1Versions(history.ContractIR) || !canonicalManifestV1Versions(history.CanonicalIR) || !canonicalManifestV1Versions(history.PhysicalSchema) || !canonicalManifestV1Versions(history.PhysicalCanonical) || !canonicalManifestV1Versions(history.MigrationManifest) || !canonicalManifestV1Versions(history.MigrationCanonical) || !canonicalManifestV1Versions(history.MigrationLedger) ||
		!canonicalManifestV1Identities(history.FactCodecs, false) || !canonicalManifestV1Identities(history.EventCodecs, false) || !canonicalManifestV1Identities(history.PrincipalSnapshotCodecs, true) {
		return false
	}
	if !subsetManifestV1(versions.FactCodecs, history.FactCodecs) || !subsetManifestV1(versions.EventCodecs, history.EventCodecs) || !subsetManifestV1(versions.PrincipalSnapshotCodecs, history.PrincipalSnapshotCodecs) || !containsManifestV1Version(history.SchemaBundles, versions.SchemaBundle) || !containsManifestV1Version(history.GeneratedManifests, versions.GeneratedManifest) {
		return false
	}
	for _, pair := range []struct {
		values []uint16
		value  uint16
	}{
		{history.ModelIR, versions.ModelIR}, {history.ContractIR, versions.ContractIR}, {history.CanonicalIR, versions.CanonicalIR}, {history.PhysicalSchema, versions.PhysicalSchema}, {history.PhysicalCanonical, versions.PhysicalCanonical}, {history.MigrationManifest, versions.MigrationManifest}, {history.MigrationCanonical, versions.MigrationCanonical}, {history.MigrationLedger, versions.MigrationLedger},
	} {
		if !containsManifestV1Version(pair.values, pair.value) {
			return false
		}
	}
	return canonicalManifestV1Identities(value.RequiredActions, true) && canonicalManifestV1Identities(value.KnownBoundaries, false)
}

func manifestV1HistoryPresent(value manifestV1History) bool {
	return value.SchemaBundles != nil && value.GeneratedManifests != nil && value.ModelIR != nil && value.ContractIR != nil && value.CanonicalIR != nil && value.PhysicalSchema != nil && value.PhysicalCanonical != nil && value.MigrationManifest != nil && value.MigrationCanonical != nil && value.MigrationLedger != nil && value.FactCodecs != nil && value.EventCodecs != nil && value.PrincipalSnapshotCodecs != nil
}

func validManifestV1Release(value manifestV1Release) bool {
	if !manifestV1CommitPattern.MatchString(value.Commit) {
		return false
	}
	if value.Development {
		return value.Version == "devel" && value.Tag == "" && value.Commit == manifestV1ZeroCommit
	}
	return semver.IsValid(value.Version) && semver.Prerelease(value.Version) == "" && value.Tag == "go/"+value.Version && value.Commit != manifestV1ZeroCommit
}

func canonicalManifestV1Identities(values []string, emptyOK bool) bool {
	if values == nil || len(values) == 0 && !emptyOK {
		return false
	}
	for index, value := range values {
		if !manifestV1ClosedIdentity.MatchString(value) || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func validManifestV1Digest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func canonicalManifestV1Versions(values []uint16) bool {
	if values == nil || len(values) == 0 {
		return false
	}
	for index, value := range values {
		if value == 0 || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func subsetManifestV1(values, set []string) bool {
	for _, value := range values {
		index := sort.SearchStrings(set, value)
		if index == len(set) || set[index] != value {
			return false
		}
	}
	return true
}

func containsManifestV1Version(values []uint16, value uint16) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= value })
	return index < len(values) && values[index] == value
}

func equalManifestV1Strings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func projectHistoricalManifestV1(value manifestV1) Manifest {
	providers := make([]Provider, len(value.Providers))
	for index, provider := range value.Providers {
		providers[index] = Provider{Provider: provider.Provider, MinimumVersion: provider.MinimumVersion, VerificationProfiles: append([]string{}, provider.VerificationProfiles...)}
	}
	return Manifest{
		FormatVersion:      FormatVersion,
		Module:             value.Module,
		Release:            Release{Development: value.Release.Development, Version: value.Release.Version, Tag: value.Release.Tag, Commit: value.Release.Commit},
		MinimumGoVersion:   value.MinimumGoVersion,
		Providers:          providers,
		DeploymentProfiles: append([]string{}, value.DeploymentProfiles...),
		Digests:            Digests{PublicGoAPI: value.Digests.PublicGoAPI, GeneratedGoABI: value.Digests.GeneratedGoABI, GraphQLABI: value.Digests.GraphQLABI, CLIJSON: value.Digests.CLIJSON, Observation: value.Digests.Observation},
		Versions: Versions{
			Generator: value.Versions.Generator, GeneratedTemplateABI: value.Versions.GeneratedTemplateABI, SchemaBundle: value.Versions.SchemaBundle, GeneratedManifest: value.Versions.GeneratedManifest, GraphQL: value.Versions.GraphQL,
			ModelIR: value.Versions.ModelIR, ContractIR: value.Versions.ContractIR, CanonicalIR: value.Versions.CanonicalIR, PhysicalSchema: value.Versions.PhysicalSchema, PhysicalCanonical: value.Versions.PhysicalCanonical,
			MigrationManifest: value.Versions.MigrationManifest, MigrationCanonical: value.Versions.MigrationCanonical, MigrationLedger: value.Versions.MigrationLedger, EventSchema: value.Versions.EventSchema,
			FactCodecs: append([]string{}, value.Versions.FactCodecs...), EventCodecs: append([]string{}, value.Versions.EventCodecs...), PrincipalSnapshotCodecs: append([]string{}, value.Versions.PrincipalSnapshotCodecs...),
		},
		HistoricalDecode: HistoricalDecode{
			SchemaBundles: append([]uint16{}, value.HistoricalDecode.SchemaBundles...), GeneratedManifests: append([]uint16{}, value.HistoricalDecode.GeneratedManifests...), GraphQL: []uint16{value.Versions.GraphQL},
			ModelIR: append([]uint16{}, value.HistoricalDecode.ModelIR...), ContractIR: append([]uint16{}, value.HistoricalDecode.ContractIR...), CanonicalIR: append([]uint16{}, value.HistoricalDecode.CanonicalIR...),
			PhysicalSchema: append([]uint16{}, value.HistoricalDecode.PhysicalSchema...), PhysicalCanonical: append([]uint16{}, value.HistoricalDecode.PhysicalCanonical...), MigrationManifest: append([]uint16{}, value.HistoricalDecode.MigrationManifest...),
			MigrationCanonical: append([]uint16{}, value.HistoricalDecode.MigrationCanonical...), MigrationLedger: append([]uint16{}, value.HistoricalDecode.MigrationLedger...), FactCodecs: append([]string{}, value.HistoricalDecode.FactCodecs...),
			EventCodecs: append([]string{}, value.HistoricalDecode.EventCodecs...), PrincipalSnapshotCodecs: append([]string{}, value.HistoricalDecode.PrincipalSnapshotCodecs...),
		},
		RequiredActions: append([]string{}, value.RequiredActions...),
		KnownBoundaries: append([]string{}, value.KnownBoundaries...),
	}
}
