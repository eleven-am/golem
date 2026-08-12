package compatibility

import (
	"bytes"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	MigrationGuidePath                 = "compatibility/migration-guide-go-v0.0.2-to-v0.1.0.json"
	MigrationGuideSHA256               = "6fff0894d4ac402e0b2eb5bbbd722d560e1e775b661f0305467aed066d4ec142"
	migrationGuideFormatVersion uint16 = 1
)

type MigrationGuide struct {
	FormatVersion   uint16                   `json:"formatVersion"`
	From            MigrationGuideEndpoint   `json:"from"`
	ToVersion       string                   `json:"toVersion"`
	RequiredActions []string                 `json:"requiredActions"`
	Corpora         []MigrationGuideCorpus   `json:"corpora"`
	Evidence        []MigrationGuideEvidence `json:"evidence"`
}

type MigrationGuideEndpoint struct{ Tag, Commit string }

func (value MigrationGuideEndpoint) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Tag    string `json:"tag"`
		Commit string `json:"commit"`
	}{value.Tag, value.Commit})
}
func (value *MigrationGuideEndpoint) UnmarshalJSON(encoded []byte) error {
	var wire struct {
		Tag    string `json:"tag"`
		Commit string `json:"commit"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fail(ReasonInvalidEncoding)
	}
	value.Tag, value.Commit = wire.Tag, wire.Commit
	return nil
}

type MigrationGuideCorpus struct {
	Path    string `json:"path"`
	GitTree string `json:"gitTree"`
}
type MigrationGuideEvidence struct {
	Identity string `json:"identity"`
	Command  string `json:"command"`
}

var (
	migrationGuidePath             = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,191}$`)
	migrationGuideTree             = regexp.MustCompile(`^[0-9a-f]{40}$`)
	migrationGuideEvidenceIdentity = regexp.MustCompile(`^github\.com/eleven-am/golem/go/[a-z0-9._/-]+:Test[A-Za-z0-9_]{1,127}$`)
	migrationGuideCommand          = regexp.MustCompile(`^go test (\./[a-z0-9._/-]+) -run '\^(Test[A-Za-z0-9_]{1,127})\$' -count=1$`)
)

func ParseMigrationGuide(encoded []byte, expectedDigest string) (MigrationGuide, error) {
	if !validDigest(expectedDigest) || digest(encoded) != expectedDigest {
		return MigrationGuide{}, fail(ReasonUntrustedDigest)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value MigrationGuide
	if err := decoder.Decode(&value); err != nil {
		return MigrationGuide{}, fail(ReasonInvalidEncoding)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return MigrationGuide{}, fail(ReasonInvalidEncoding)
	}
	if value.FormatVersion != migrationGuideFormatVersion {
		return MigrationGuide{}, fail(ReasonUnsupportedFormat)
	}
	if !validMigrationGuide(value) {
		return MigrationGuide{}, fail(ReasonInvalidManifest)
	}
	canonical, err := EncodeMigrationGuide(value)
	if err != nil {
		return MigrationGuide{}, err
	}
	if !bytes.Equal(canonical, encoded) {
		return MigrationGuide{}, fail(ReasonNoncanonical)
	}
	return cloneMigrationGuide(value), nil
}

func EncodeMigrationGuide(value MigrationGuide) ([]byte, error) {
	if value.FormatVersion != migrationGuideFormatVersion {
		return nil, fail(ReasonUnsupportedFormat)
	}
	if !validMigrationGuide(value) {
		return nil, fail(ReasonInvalidManifest)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fail(ReasonInvalidEncoding)
	}
	return append(encoded, '\n'), nil
}

func MigrationGuideDigest(encoded []byte) string { return digest(encoded) }

func ValidateMigrationGuideTransition(guide MigrationGuide, authority MigrationGuideAuthority, previousTag, previousCommit, currentTag, currentVersion string, actions []string) error {
	if !validMigrationGuide(guide) || !validMigrationGuideAuthority(authority) ||
		authority.FromTag != previousTag || guide.From.Tag != previousTag || guide.From.Commit != previousCommit ||
		!semver.IsValid(currentVersion) || semver.Prerelease(currentVersion) != "" || currentTag != "go/"+currentVersion || semver.Compare(strings.TrimPrefix(previousTag, "go/"), currentVersion) >= 0 || authority.ToVersion != currentVersion || guide.ToVersion != currentVersion || !equalStrings(actions, guide.RequiredActions) {
		return fail(ReasonIncompatible)
	}
	return nil
}

func validMigrationGuideAuthority(value MigrationGuideAuthority) bool {
	fromVersion := strings.TrimPrefix(value.FromTag, "go/")
	return strings.HasPrefix(value.Path, "compatibility/") && safeGuidePath(value.Path) && validDigest(value.SHA256) &&
		semver.IsValid(fromVersion) && semver.Prerelease(fromVersion) == "" && value.FromTag == "go/"+fromVersion &&
		semver.IsValid(value.ToVersion) && semver.Prerelease(value.ToVersion) == "" && semver.Compare(fromVersion, value.ToVersion) < 0
}

func safeGuidePath(value string) bool {
	return migrationGuidePath.MatchString(value) && path.Clean(value) == value && value[0] != '/' && !bytes.Contains([]byte(value), []byte(".."))
}

func validMigrationGuide(value MigrationGuide) bool {
	version := strings.TrimPrefix(value.From.Tag, "go/")
	if value.FormatVersion != 1 || !semver.IsValid(version) || semver.Prerelease(version) != "" || value.From.Tag != "go/"+version || !commitPattern.MatchString(value.From.Commit) ||
		!semver.IsValid(value.ToVersion) || semver.Prerelease(value.ToVersion) != "" || semver.Compare(version, value.ToVersion) >= 0 ||
		!canonicalIdentities(value.RequiredActions, false) || !hasIdentity(value.RequiredActions, "migration-guide.execute") || len(value.Corpora) == 0 || len(value.Evidence) == 0 {
		return false
	}
	for index, corpus := range value.Corpora {
		if !safeGuidePath(corpus.Path) || !migrationGuideTree.MatchString(corpus.GitTree) || index > 0 && value.Corpora[index-1].Path >= corpus.Path {
			return false
		}
	}
	for index, evidence := range value.Evidence {
		command := migrationGuideCommand.FindStringSubmatch(evidence.Command)
		if !migrationGuideEvidenceIdentity.MatchString(evidence.Identity) || len(command) != 3 || !safeGuidePath(strings.TrimPrefix(command[1], "./")) || index > 0 && value.Evidence[index-1].Identity >= evidence.Identity {
			return false
		}
		identityPath := strings.TrimPrefix(evidence.Identity[:strings.LastIndexByte(evidence.Identity, ':')], Module+"/")
		test := evidence.Identity[strings.LastIndexByte(evidence.Identity, ':')+1:]
		if command[1] != "./"+identityPath || command[2] != test {
			return false
		}
	}
	return true
}

func cloneMigrationGuide(value MigrationGuide) MigrationGuide {
	encoded, _ := json.Marshal(value)
	var result MigrationGuide
	_ = json.Unmarshal(encoded, &result)
	return result
}
