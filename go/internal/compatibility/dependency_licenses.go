package compatibility

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strings"
)

const (
	DependencyLicenseAuthorityPath   = "compatibility/dependency-licenses.json"
	DependencyLicenseAuthoritySHA256 = "f5c8341f5d50f78d9f3b8dc2ae15adb9d9866a1f1cd3495ef83ef354525b0e65"
	ProjectLicensePath               = "LICENSE"
	ThirdPartyNoticesPath            = "THIRD_PARTY_NOTICES"
	ProjectLicenseDeclared           = "LicenseRef-Golem-GPLv3-Unspecified"
	dependencyLicenseFormatVersion   = 1
)

type DependencyLicenseAuthority struct {
	FormatVersion uint16                     `json:"formatVersion"`
	Project       ProjectLicenseAuthority    `json:"project"`
	Notices       ThirdPartyNoticesAuthority `json:"notices"`
	Dependencies  []DependencyLicense        `json:"dependencies"`
}

type ProjectLicenseAuthority struct {
	Module          string `json:"module"`
	LicenseDeclared string `json:"licenseDeclared"`
	Path            string `json:"path"`
	SHA256          string `json:"sha256"`
}

type ThirdPartyNoticesAuthority struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type DependencyLicense struct {
	Module          string `json:"module"`
	Version         string `json:"version"`
	LicenseDeclared string `json:"licenseDeclared"`
	LicenseSHA256   string `json:"licenseSHA256"`
}

var (
	dependencyModule  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~/-]{0,255}$`)
	dependencyVersion = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:[-+][A-Za-z0-9.-]+)?$`)
	spdxDeclaration   = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9.-]*|LicenseRef-[A-Za-z0-9][A-Za-z0-9.-]*)$`)
)

func ParseDependencyLicenseAuthority(encoded []byte, expectedDigest string) (DependencyLicenseAuthority, error) {
	if !validDigest(expectedDigest) || digest(encoded) != expectedDigest {
		return DependencyLicenseAuthority{}, fail(ReasonUntrustedDigest)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value DependencyLicenseAuthority
	if err := decoder.Decode(&value); err != nil {
		return DependencyLicenseAuthority{}, fail(ReasonInvalidEncoding)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return DependencyLicenseAuthority{}, fail(ReasonInvalidEncoding)
	}
	if value.FormatVersion != dependencyLicenseFormatVersion {
		return DependencyLicenseAuthority{}, fail(ReasonUnsupportedFormat)
	}
	if !validDependencyLicenseAuthority(value) {
		return DependencyLicenseAuthority{}, fail(ReasonInvalidManifest)
	}
	canonical, err := EncodeDependencyLicenseAuthority(value)
	if err != nil {
		return DependencyLicenseAuthority{}, err
	}
	if !bytes.Equal(canonical, encoded) {
		return DependencyLicenseAuthority{}, fail(ReasonNoncanonical)
	}
	return cloneDependencyLicenseAuthority(value), nil
}

func EncodeDependencyLicenseAuthority(value DependencyLicenseAuthority) ([]byte, error) {
	if value.FormatVersion != dependencyLicenseFormatVersion {
		return nil, fail(ReasonUnsupportedFormat)
	}
	if !validDependencyLicenseAuthority(value) {
		return nil, fail(ReasonInvalidManifest)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fail(ReasonInvalidEncoding)
	}
	return append(encoded, '\n'), nil
}

func DependencyLicenseAuthorityDigest(encoded []byte) string { return digest(encoded) }

func ValidateDependencyLicenseFiles(authority DependencyLicenseAuthority, projectLicense, notices []byte) error {
	if !validDependencyLicenseAuthority(authority) || digest(projectLicense) != authority.Project.SHA256 || digest(notices) != authority.Notices.SHA256 {
		return fail(ReasonUntrustedDigest)
	}
	if bytes.Contains(projectLicense, []byte{'\r'}) || len(projectLicense) == 0 || projectLicense[len(projectLicense)-1] != '\n' || bytes.Contains(notices, []byte{'\r'}) || len(notices) == 0 || notices[len(notices)-1] != '\n' {
		return fail(ReasonNoncanonical)
	}
	prefix := []byte("THIRD-PARTY NOTICES\nFormat-Version: 1\n\n")
	if !bytes.HasPrefix(notices, prefix) {
		return fail(ReasonInvalidManifest)
	}
	for _, dependency := range authority.Dependencies {
		license, ok := DependencyLicenseText(notices, dependency)
		if !ok || digest(license) != dependency.LicenseSHA256 {
			return fail(ReasonUntrustedDigest)
		}
	}
	if bytes.Count(notices, []byte("----- BEGIN LICENSE ")) != len(authority.Dependencies) || bytes.Count(notices, []byte("----- END LICENSE ")) != len(authority.Dependencies) {
		return fail(ReasonInvalidManifest)
	}
	return nil
}

func DependencyLicenseText(notices []byte, dependency DependencyLicense) ([]byte, bool) {
	begin := []byte("----- BEGIN LICENSE " + dependency.Module + "@" + dependency.Version + " -----\n")
	end := []byte("----- END LICENSE " + dependency.Module + "@" + dependency.Version + " -----\n")
	if bytes.Count(notices, begin) != 1 || bytes.Count(notices, end) != 1 {
		return nil, false
	}
	start := bytes.Index(notices, begin) + len(begin)
	finish := bytes.Index(notices[start:], end)
	if finish < 0 {
		return nil, false
	}
	return append([]byte(nil), notices[start:start+finish]...), true
}

func (value DependencyLicenseAuthority) LicenseFor(module, version string) (string, bool) {
	if module == value.Project.Module && version == "devel" {
		return value.Project.LicenseDeclared, true
	}
	index := sort.Search(len(value.Dependencies), func(index int) bool { return value.Dependencies[index].Module >= module })
	if index == len(value.Dependencies) || value.Dependencies[index].Module != module || value.Dependencies[index].Version != version {
		return "", false
	}
	return value.Dependencies[index].LicenseDeclared, true
}

func validDependencyLicenseAuthority(value DependencyLicenseAuthority) bool {
	if value.FormatVersion != dependencyLicenseFormatVersion || value.Project.Module != Module || value.Project.LicenseDeclared != ProjectLicenseDeclared || value.Project.Path != ProjectLicensePath || value.Notices.Path != ThirdPartyNoticesPath || !validDigest(value.Project.SHA256) || !validDigest(value.Notices.SHA256) || len(value.Dependencies) == 0 {
		return false
	}
	for index, dependency := range value.Dependencies {
		if !dependencyModule.MatchString(dependency.Module) || !dependencyVersion.MatchString(dependency.Version) || !spdxDeclaration.MatchString(dependency.LicenseDeclared) || !validDigest(dependency.LicenseSHA256) || dependency.Module == value.Project.Module || index > 0 && strings.Compare(value.Dependencies[index-1].Module, dependency.Module) >= 0 {
			return false
		}
	}
	return true
}

func cloneDependencyLicenseAuthority(value DependencyLicenseAuthority) DependencyLicenseAuthority {
	result := value
	result.Dependencies = append([]DependencyLicense(nil), value.Dependencies...)
	return result
}
