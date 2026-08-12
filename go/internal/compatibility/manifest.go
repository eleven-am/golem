// Package compatibility owns the strict release compatibility manifest and
// semantic release comparison used by P8 release tooling. It is internal: the
// application runtime consumes generated schema identities, not this release
// artifact.
package compatibility

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	FormatVersion uint16 = 2
	Module               = "github.com/eleven-am/golem/go"
)

type Reason string

const (
	ReasonInvalidEncoding   Reason = "invalid_encoding"
	ReasonUnsupportedFormat Reason = "unsupported_format"
	ReasonInvalidManifest   Reason = "invalid_manifest"
	ReasonNoncanonical      Reason = "noncanonical"
	ReasonUntrustedDigest   Reason = "untrusted_digest"
	ReasonIncompatible      Reason = "incompatible"
)

type Error struct{ Reason Reason }

func (failure *Error) Error() string {
	if failure == nil {
		return "P8_COMPATIBILITY: invalid_manifest"
	}
	return "P8_COMPATIBILITY: " + string(failure.Reason)
}

func fail(reason Reason) error { return &Error{Reason: reason} }

type Manifest struct {
	FormatVersion      uint16                   `json:"formatVersion"`
	Module             string                   `json:"module"`
	Release            Release                  `json:"release"`
	MinimumGoVersion   string                   `json:"minimumGoVersion"`
	Providers          []Provider               `json:"providers"`
	DeploymentProfiles []string                 `json:"deploymentProfiles"`
	Digests            Digests                  `json:"digests"`
	Versions           Versions                 `json:"versions"`
	HistoricalDecode   HistoricalDecode         `json:"historicalDecode"`
	MigrationGuide     *MigrationGuideAuthority `json:"migrationGuide"`
	RequiredActions    []string                 `json:"requiredActions"`
	KnownBoundaries    []string                 `json:"knownBoundaries"`
}

type Release struct {
	Development bool   `json:"development"`
	Version     string `json:"version"`
	Tag         string `json:"tag"`
	Commit      string `json:"commit"`
}

type Provider struct {
	Provider             string   `json:"provider"`
	MinimumVersion       string   `json:"minimumVersion"`
	VerificationProfiles []string `json:"verificationProfiles"`
}

type Digests struct {
	PublicGoAPI    string `json:"publicGoAPI"`
	GeneratedGoABI string `json:"generatedGoABI"`
	GraphQLABI     string `json:"graphQLABI"`
	CLIJSON        string `json:"cliJSON"`
	Observation    string `json:"observation"`
}

type MigrationGuideAuthority struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	FromTag string `json:"fromTag"`
	ToMajor uint16 `json:"toMajor"`
}

type Versions struct {
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

type HistoricalDecode struct {
	SchemaBundles           []uint16 `json:"schemaBundles"`
	GeneratedManifests      []uint16 `json:"generatedManifests"`
	GraphQL                 []uint16 `json:"graphQL"`
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
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	goVersion      = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*))?$`)
	serverVersion  = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*))?$`)
	closedIdentity = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/@+-]{0,127}$`)
	zeroCommit     = strings.Repeat("0", 40)
)

// Parse validates exact canonical bytes and a digest obtained through a
// separate trust channel. The expected digest is never read from the JSON.
func Parse(encoded []byte, expectedDigest string) (Manifest, error) {
	if !validDigest(expectedDigest) || digest(encoded) != expectedDigest {
		return Manifest{}, fail(ReasonUntrustedDigest)
	}
	return parseCurrentManifest(encoded)
}

func parseCurrentManifest(encoded []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value Manifest
	if err := decoder.Decode(&value); err != nil {
		return Manifest{}, fail(ReasonInvalidEncoding)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fail(ReasonInvalidEncoding)
	}
	if value.FormatVersion != FormatVersion {
		return Manifest{}, fail(ReasonUnsupportedFormat)
	}
	if err := validate(value); err != nil {
		return Manifest{}, err
	}
	canonical, err := Encode(value)
	if err != nil {
		return Manifest{}, err
	}
	if !bytes.Equal(canonical, encoded) {
		return Manifest{}, fail(ReasonNoncanonical)
	}
	return clone(value), nil
}

func Encode(value Manifest) ([]byte, error) {
	if value.FormatVersion != FormatVersion {
		return nil, fail(ReasonUnsupportedFormat)
	}
	if err := validate(value); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fail(ReasonInvalidEncoding)
	}
	return append(encoded, '\n'), nil
}

func Digest(encoded []byte) string { return digest(encoded) }

func digest(encoded []byte) string {
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validate(value Manifest) error {
	if value.Module != Module || !validRelease(value.Release) || !goVersion.MatchString(value.MinimumGoVersion) || len(value.Providers) != 2 {
		return fail(ReasonInvalidManifest)
	}
	for index, provider := range value.Providers {
		if (provider.Provider != "postgresql" && provider.Provider != "sqlite") || !serverVersion.MatchString(provider.MinimumVersion) || !canonicalIdentities(provider.VerificationProfiles, false) {
			return fail(ReasonInvalidManifest)
		}
		if index > 0 && value.Providers[index-1].Provider >= provider.Provider {
			return fail(ReasonInvalidManifest)
		}
	}
	if !equalStrings(value.DeploymentProfiles, []string{"adapted-multi-process", "database-backed-single-process", "embedded-single-process"}) ||
		!equalStrings(value.Providers[0].VerificationProfiles, []string{"c", "linguistic"}) ||
		!equalStrings(value.Providers[1].VerificationProfiles, []string{"file", "named-shared-memory"}) {
		return fail(ReasonInvalidManifest)
	}
	for _, value := range []string{value.Digests.PublicGoAPI, value.Digests.GeneratedGoABI, value.Digests.GraphQLABI, value.Digests.CLIJSON, value.Digests.Observation} {
		if !validDigest(value) {
			return fail(ReasonInvalidManifest)
		}
	}
	versions := value.Versions
	if !closedIdentity.MatchString(versions.Generator) || !closedIdentity.MatchString(versions.GeneratedTemplateABI) ||
		versions.SchemaBundle == 0 || versions.GeneratedManifest == 0 || versions.GraphQL == 0 || versions.ModelIR == 0 || versions.ContractIR == 0 || versions.CanonicalIR == 0 ||
		versions.PhysicalSchema == 0 || versions.PhysicalCanonical == 0 || versions.MigrationManifest == 0 || versions.MigrationCanonical == 0 || versions.MigrationLedger == 0 || versions.EventSchema == 0 ||
		!canonicalIdentities(versions.FactCodecs, false) || !canonicalIdentities(versions.EventCodecs, false) || !canonicalIdentities(versions.PrincipalSnapshotCodecs, true) {
		return fail(ReasonInvalidManifest)
	}
	history := value.HistoricalDecode
	if !canonicalVersions(history.SchemaBundles) || !canonicalVersions(history.GeneratedManifests) || !canonicalVersions(history.GraphQL) || !canonicalVersions(history.ModelIR) || !canonicalVersions(history.ContractIR) || !canonicalVersions(history.CanonicalIR) ||
		!canonicalVersions(history.PhysicalSchema) || !canonicalVersions(history.PhysicalCanonical) || !canonicalVersions(history.MigrationManifest) ||
		!canonicalVersions(history.MigrationCanonical) || !canonicalVersions(history.MigrationLedger) ||
		!canonicalIdentities(history.FactCodecs, false) || !canonicalIdentities(history.EventCodecs, false) || !canonicalIdentities(history.PrincipalSnapshotCodecs, true) {
		return fail(ReasonInvalidManifest)
	}
	if !isSubset(versions.FactCodecs, history.FactCodecs) || !isSubset(versions.EventCodecs, history.EventCodecs) || !isSubset(versions.PrincipalSnapshotCodecs, history.PrincipalSnapshotCodecs) || !containsVersion(history.SchemaBundles, versions.SchemaBundle) || !containsVersion(history.GeneratedManifests, versions.GeneratedManifest) || !containsVersion(history.GraphQL, versions.GraphQL) {
		return fail(ReasonInvalidManifest)
	}
	for _, pair := range []struct {
		values []uint16
		value  uint16
	}{
		{history.ModelIR, versions.ModelIR}, {history.ContractIR, versions.ContractIR}, {history.CanonicalIR, versions.CanonicalIR},
		{history.PhysicalSchema, versions.PhysicalSchema}, {history.PhysicalCanonical, versions.PhysicalCanonical},
		{history.MigrationManifest, versions.MigrationManifest}, {history.MigrationCanonical, versions.MigrationCanonical},
		{history.MigrationLedger, versions.MigrationLedger},
	} {
		if !containsVersion(pair.values, pair.value) {
			return fail(ReasonInvalidManifest)
		}
	}
	if !canonicalIdentities(value.RequiredActions, true) || !canonicalIdentities(value.KnownBoundaries, false) {
		return fail(ReasonInvalidManifest)
	}
	guideRequired := hasIdentity(value.RequiredActions, "migration-guide.execute")
	if guideRequired != (value.MigrationGuide != nil) || value.MigrationGuide != nil && !validMigrationGuideAuthority(*value.MigrationGuide) {
		return fail(ReasonInvalidManifest)
	}
	return nil
}

func validRelease(value Release) bool {
	if !commitPattern.MatchString(value.Commit) {
		return false
	}
	if value.Development {
		return value.Version == "devel" && value.Tag == "" && value.Commit == zeroCommit
	}
	return semver.IsValid(value.Version) && semver.Prerelease(value.Version) == "" && value.Tag == "go/"+value.Version && value.Commit != zeroCommit
}

func canonicalIdentities(values []string, emptyOK bool) bool {
	if values == nil {
		return false
	}
	if len(values) == 0 {
		return emptyOK
	}
	for index, value := range values {
		if !closedIdentity.MatchString(value) || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func canonicalVersions(values []uint16) bool {
	if len(values) == 0 {
		return false
	}
	for index, value := range values {
		if value == 0 || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func isSubset(values, set []string) bool {
	for _, value := range values {
		index := sort.SearchStrings(set, value)
		if index == len(set) || set[index] != value {
			return false
		}
	}
	return true
}

func containsVersion(values []uint16, value uint16) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= value })
	return index < len(values) && values[index] == value
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func clone(value Manifest) Manifest {
	encoded, _ := json.Marshal(value)
	var result Manifest
	_ = json.Unmarshal(encoded, &result)
	return result
}

func CodeOf(err error) (Reason, bool) {
	var failure *Error
	if !errors.As(err, &failure) {
		return "", false
	}
	switch failure.Reason {
	case ReasonInvalidEncoding, ReasonUnsupportedFormat, ReasonInvalidManifest, ReasonNoncanonical, ReasonUntrustedDigest, ReasonIncompatible:
		return failure.Reason, true
	default:
		return "", false
	}
}

type ReleaseLevel uint8

const (
	ReleasePatch ReleaseLevel = iota + 1
	ReleaseMinor
	ReleaseMajor
)

type LayerAssessments struct {
	PublicGoAPI    LayerChange
	GeneratedGoABI LayerChange
	GraphQLABI     LayerChange
	CLIJSON        LayerChange
	Observation    LayerChange
}

type Transition struct {
	Actual   ReleaseLevel
	Required ReleaseLevel
}

// CompareRelease proves the version bump is at least as large as every
// independently classified layer change and that required operator actions
// are explicit. It never derives semantics from digest inequality alone.
func CompareRelease(previous, current Manifest, layers LayerAssessments) (Transition, error) {
	if err := validate(previous); err != nil {
		return Transition{}, err
	}
	if err := validate(current); err != nil {
		return Transition{}, err
	}
	actual, ok := releaseTransition(previous.Release, current.Release)
	if !ok {
		return Transition{}, fail(ReasonIncompatible)
	}
	required := ReleasePatch
	for _, layer := range []struct {
		before, after string
		change        LayerChange
	}{
		{previous.Digests.PublicGoAPI, current.Digests.PublicGoAPI, layers.PublicGoAPI},
		{previous.Digests.GeneratedGoABI, current.Digests.GeneratedGoABI, layers.GeneratedGoABI},
		{previous.Digests.GraphQLABI, current.Digests.GraphQLABI, layers.GraphQLABI},
		{previous.Digests.CLIJSON, current.Digests.CLIJSON, layers.CLIJSON},
		{previous.Digests.Observation, current.Digests.Observation, layers.Observation},
	} {
		if !validLayerAssessment(layer.before, layer.after, layer.change) {
			return Transition{}, fail(ReasonIncompatible)
		}
		required = maximumLevel(required, layerLevel(layer.change))
	}

	generatedChanged := previous.Versions.Generator != current.Versions.Generator || previous.Versions.GeneratedTemplateABI != current.Versions.GeneratedTemplateABI ||
		previous.Versions.SchemaBundle != current.Versions.SchemaBundle || previous.Versions.GraphQL != current.Versions.GraphQL ||
		previous.Versions.ModelIR != current.Versions.ModelIR || previous.Versions.ContractIR != current.Versions.ContractIR || previous.Versions.CanonicalIR != current.Versions.CanonicalIR
	databaseChanged := previous.Versions.PhysicalSchema != current.Versions.PhysicalSchema || previous.Versions.PhysicalCanonical != current.Versions.PhysicalCanonical ||
		previous.Versions.MigrationManifest != current.Versions.MigrationManifest || previous.Versions.MigrationCanonical != current.Versions.MigrationCanonical || previous.Versions.MigrationLedger != current.Versions.MigrationLedger

	formatLevel := compareFormatHistory(previous, current)
	required = maximumLevel(required, formatLevel)
	providerLevel := compareProviders(previous, current)
	required = maximumLevel(required, providerLevel)
	if !reflect.DeepEqual(previous.RequiredActions, current.RequiredActions) || !reflect.DeepEqual(previous.KnownBoundaries, current.KnownBoundaries) {
		required = maximumLevel(required, ReleaseMinor)
	}
	if generatedChanged || layers.GeneratedGoABI != LayerUnchanged || layers.GraphQLABI != LayerUnchanged {
		if !hasIdentity(current.RequiredActions, "regenerate.generated") {
			return Transition{Actual: actual, Required: required}, fail(ReasonIncompatible)
		}
	}
	if databaseChanged {
		if !hasIdentity(current.RequiredActions, "migrate.database") {
			return Transition{Actual: actual, Required: required}, fail(ReasonIncompatible)
		}
	}
	if providerLevel > ReleasePatch && !hasIdentity(current.RequiredActions, "upgrade.operator") {
		return Transition{Actual: actual, Required: required}, fail(ReasonIncompatible)
	}
	if required == ReleaseMajor && !hasIdentity(current.RequiredActions, "migration-guide.execute") {
		return Transition{Actual: actual, Required: required}, fail(ReasonIncompatible)
	}
	if actual < required {
		return Transition{Actual: actual, Required: required}, fail(ReasonIncompatible)
	}
	return Transition{Actual: actual, Required: required}, nil
}

func validLayerAssessment(before, after string, change LayerChange) bool {
	if before == after {
		return change == LayerUnchanged
	}
	return change == LayerAdditive || change == LayerBreaking
}

func layerLevel(change LayerChange) ReleaseLevel {
	switch change {
	case LayerUnchanged:
		return ReleasePatch
	case LayerAdditive:
		return ReleaseMinor
	default:
		return ReleaseMajor
	}
}

func maximumLevel(left, right ReleaseLevel) ReleaseLevel {
	if right > left {
		return right
	}
	return left
}

func releaseTransition(previous, current Release) (ReleaseLevel, bool) {
	if previous.Development || current.Development || !semver.IsValid(previous.Version) || !semver.IsValid(current.Version) || semver.Compare(current.Version, previous.Version) <= 0 {
		return 0, false
	}
	if semver.Major(current.Version) != semver.Major(previous.Version) {
		return ReleaseMajor, true
	}
	if semver.MajorMinor(current.Version) != semver.MajorMinor(previous.Version) {
		return ReleaseMinor, true
	}
	return ReleasePatch, true
}

func compareFormatHistory(previous, current Manifest) ReleaseLevel {
	level := ReleasePatch
	for _, pair := range []struct {
		beforeCurrent, afterCurrent uint16
		beforeHistory, afterHistory []uint16
	}{
		{previous.Versions.SchemaBundle, current.Versions.SchemaBundle, previous.HistoricalDecode.SchemaBundles, current.HistoricalDecode.SchemaBundles},
		{previous.Versions.GeneratedManifest, current.Versions.GeneratedManifest, previous.HistoricalDecode.GeneratedManifests, current.HistoricalDecode.GeneratedManifests},
		{previous.Versions.GraphQL, current.Versions.GraphQL, previous.HistoricalDecode.GraphQL, current.HistoricalDecode.GraphQL},
		{previous.Versions.ModelIR, current.Versions.ModelIR, previous.HistoricalDecode.ModelIR, current.HistoricalDecode.ModelIR},
		{previous.Versions.ContractIR, current.Versions.ContractIR, previous.HistoricalDecode.ContractIR, current.HistoricalDecode.ContractIR},
		{previous.Versions.CanonicalIR, current.Versions.CanonicalIR, previous.HistoricalDecode.CanonicalIR, current.HistoricalDecode.CanonicalIR},
		{previous.Versions.PhysicalSchema, current.Versions.PhysicalSchema, previous.HistoricalDecode.PhysicalSchema, current.HistoricalDecode.PhysicalSchema},
		{previous.Versions.PhysicalCanonical, current.Versions.PhysicalCanonical, previous.HistoricalDecode.PhysicalCanonical, current.HistoricalDecode.PhysicalCanonical},
		{previous.Versions.MigrationManifest, current.Versions.MigrationManifest, previous.HistoricalDecode.MigrationManifest, current.HistoricalDecode.MigrationManifest},
		{previous.Versions.MigrationCanonical, current.Versions.MigrationCanonical, previous.HistoricalDecode.MigrationCanonical, current.HistoricalDecode.MigrationCanonical},
		{previous.Versions.MigrationLedger, current.Versions.MigrationLedger, previous.HistoricalDecode.MigrationLedger, current.HistoricalDecode.MigrationLedger},
	} {
		if !versionsRetained(pair.beforeHistory, pair.afterHistory) || !containsVersion(pair.afterHistory, pair.beforeCurrent) {
			return ReleaseMajor
		}
		if pair.beforeCurrent != pair.afterCurrent || !reflect.DeepEqual(pair.beforeHistory, pair.afterHistory) {
			level = ReleaseMinor
		}
	}
	for _, pair := range []struct {
		beforeCurrent, afterCurrent []string
		beforeHistory, afterHistory []string
	}{
		{previous.Versions.FactCodecs, current.Versions.FactCodecs, previous.HistoricalDecode.FactCodecs, current.HistoricalDecode.FactCodecs},
		{previous.Versions.EventCodecs, current.Versions.EventCodecs, previous.HistoricalDecode.EventCodecs, current.HistoricalDecode.EventCodecs},
		{previous.Versions.PrincipalSnapshotCodecs, current.Versions.PrincipalSnapshotCodecs, previous.HistoricalDecode.PrincipalSnapshotCodecs, current.HistoricalDecode.PrincipalSnapshotCodecs},
	} {
		if !isSubset(pair.beforeHistory, pair.afterHistory) || !isSubset(pair.beforeCurrent, pair.afterHistory) {
			return ReleaseMajor
		}
		if !reflect.DeepEqual(pair.beforeCurrent, pair.afterCurrent) || !reflect.DeepEqual(pair.beforeHistory, pair.afterHistory) {
			level = ReleaseMinor
		}
	}
	return level
}

func compareProviders(previous, current Manifest) ReleaseLevel {
	level := ReleasePatch
	for index := range previous.Providers {
		before, after := previous.Providers[index], current.Providers[index]
		if before.Provider != after.Provider || !isSubset(before.VerificationProfiles, after.VerificationProfiles) {
			return ReleaseMajor
		}
		if before.MinimumVersion != after.MinimumVersion {
			beforeSemver, afterSemver := "v"+before.MinimumVersion, "v"+after.MinimumVersion
			if !semver.IsValid(beforeSemver) || !semver.IsValid(afterSemver) || semver.MajorMinor(beforeSemver) != semver.MajorMinor(afterSemver) || semver.Compare(afterSemver, beforeSemver) < 0 {
				return ReleaseMajor
			}
		}
		if !reflect.DeepEqual(before.VerificationProfiles, after.VerificationProfiles) {
			level = ReleaseMinor
		}
	}
	if !isSubset(previous.DeploymentProfiles, current.DeploymentProfiles) {
		return ReleaseMajor
	}
	if !reflect.DeepEqual(previous.DeploymentProfiles, current.DeploymentProfiles) {
		level = ReleaseMinor
	}
	return level
}

func versionsRetained(previous, current []uint16) bool {
	for _, value := range previous {
		if !containsVersion(current, value) {
			return false
		}
	}
	return true
}

func hasIdentity(values []string, identity string) bool {
	index := sort.SearchStrings(values, identity)
	return index < len(values) && values[index] == identity
}
