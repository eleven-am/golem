package p8mutation

import "time"

// dependencyLicenseReleaseMutations is isolated from the global catalog until
// the Order-7 dependency-license and release-packaging closure is reviewed.
func dependencyLicenseReleaseMutations() []Mutation {
	gate := func(pkg, test string) Gate {
		return Gate{Directory: "go", Package: pkg, Test: test}
	}
	mutation := func(label, summary, path, before, after, pkg, test string) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: []Patch{{Path: path, Before: before, After: after}}, Gate: gate(pkg, test), Timeout: 8 * time.Minute}
	}
	return []Mutation{
		mutation(
			"DEPENDENCY_LICENSE_ACCEPTS_NOTICE_DIGEST_MISMATCH",
			"accept third-party notice bytes that do not match the signed canonical notice digest",
			"go/internal/compatibility/dependency_licenses.go",
			"digest(notices) != authority.Notices.SHA256",
			"false && digest(notices) != authority.Notices.SHA256",
			"./internal/compatibility", "TestDependencyLicenseAuthorityRejectsHostileEvidence",
		),
		mutation(
			"DEPENDENCY_LICENSE_ACCEPTS_TRUNCATED_COMPOSITE_TEXT",
			"accept a notice section whose verbatim reviewed dependency license text was truncated",
			"go/internal/compatibility/dependency_licenses.go",
			"digest(license) != dependency.LicenseSHA256",
			"false && digest(license) != dependency.LicenseSHA256",
			"./internal/compatibility", "TestDependencyLicenseAuthorityRejectsHostileEvidence",
		),
		mutation(
			"RELEASE_LICENSE_IGNORES_SELECTED_MODULE_VERSION",
			"seal dependency-license authority for a different module version than the signed go.mod selects",
			"go/internal/release/release.go",
			"if selected[dependency.Module] != dependency.Version {",
			"if false && selected[dependency.Module] != dependency.Version {",
			"./internal/release", "TestP8SignedCandidateRejectsMissingTamperedAndNonregularLicenseEvidence",
		),
		mutation(
			"RELEASE_ARCHIVE_OMITS_THIRD_PARTY_NOTICES",
			"ship platform archives without the sealed third-party distribution notices",
			"go/internal/release/release.go",
			"\t\t{Name: compatibility.ThirdPartyNoticesPath, Mode: 0o644, Content: config.Candidate.thirdPartyNotices},\n",
			"",
			"./internal/release", "TestP8ReleaseArtifactReproducibility",
		),
		mutation(
			"RELEASE_SOURCE_SPDX_OMITS_DECLARED_LICENSE",
			"replace reviewed source dependency license declarations with NOASSERTION",
			"go/internal/release/release.go",
			"LicenseDeclared: declared,",
			"LicenseDeclared: \"NOASSERTION\",",
			"./internal/release", "TestP8ReleaseArtifactReproducibility",
		),
		mutation(
			"RELEASE_SOURCE_SPDX_OMITS_EXTRACTED_LICENSE_TEXT",
			"declare custom source LicenseRefs without their exact SPDX extracted license texts",
			"go/internal/release/release.go",
			"ExtractedLicenses: extracted,",
			"ExtractedLicenses: nil,",
			"./internal/release", "TestP8ReleaseArtifactReproducibility",
		),
		mutation(
			"RELEASE_BINARY_SPDX_OMITS_PROJECT_LICENSE",
			"replace the conservative project license reference in binary SPDX with NOASSERTION",
			"go/internal/release/release.go",
			"LicenseDeclared: licenseDeclared,",
			"LicenseDeclared: \"NOASSERTION\",",
			"./internal/release", "TestP8ReleaseArtifactReproducibility",
		),
		mutation(
			"RELEASE_BINARY_SPDX_OMITS_EXTRACTED_LICENSE_TEXT",
			"declare the project LicenseRef in binary SPDX without its exact extracted GPLv3 text",
			"go/internal/release/release.go",
			"ExtractedLicenses: []spdxExtractedLicense{{LicenseID: licenseDeclared, ExtractedText: string(candidate.projectLicense), Name: \"Golem GPLv3 text; only/or-later unspecified\"}},",
			"ExtractedLicenses: nil,",
			"./internal/release", "TestP8ReleaseArtifactReproducibility",
		),
		mutation(
			"RELEASE_PROVENANCE_OMITS_LICENSE_AUTHORITY",
			"omit the signed dependency-license authority from release provenance",
			"go/internal/release/release.go",
			"\t\t{URI: \"file:\" + compatibility.DependencyLicenseAuthorityPath, Digest: map[string]string{\"sha256\": compatibility.DependencyLicenseAuthoritySHA256}},\n",
			"",
			"./internal/release", "TestP8ReleaseArtifactReproducibility",
		),
		mutation(
			"RELEASE_PUBLISH_IGNORES_STAGED_LICENSE_EVIDENCE",
			"publish a staged release after its sealed license or notices were removed or replaced",
			"go/internal/release/release.go",
			"if err := verifyBuiltLicenseEvidence(staged, candidate); err != nil {",
			"if err := verifyBuiltLicenseEvidence(staged, candidate); false && err != nil {",
			"./internal/release", "TestP8ExistingVersionArtifactReplacementRefused",
		),
	}
}
