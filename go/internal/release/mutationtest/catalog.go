package mutationtest

import (
	"time"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

const sourceRelease = "go/internal/release/release.go"

func Catalog() []p8mutation.Mutation {
	gate := func(test string) p8mutation.Gate {
		return p8mutation.Gate{Directory: "go", Package: "./internal/release", Test: test}
	}
	mutation := func(label, summary, before, after, test string) p8mutation.Mutation {
		return p8mutation.Mutation{
			Label: label, Summary: summary,
			Patches: []p8mutation.Patch{{Path: sourceRelease, Before: before, After: after}},
			Gate:    gate(test), Timeout: 3 * time.Minute,
		}
	}
	return []p8mutation.Mutation{
		mutation(
			"RELEASE_TRANSITION_SELECTS_LEXICALLY_EARLIER_BASELINE",
			"select the lowest strict semantic version instead of the greatest lower release",
			"if bestVersion == \"\" || semver.Compare(version, bestVersion) > 0 {",
			"if bestVersion == \"\" || semver.Compare(version, bestVersion) < 0 {",
			"TestP8CompatibilityBaselineSelectsGreatestSignedLowerTagAndClosesCompetitors",
		),
		mutation(
			"RELEASE_TRANSITION_IGNORES_COMPETITOR_SIGNATURE",
			"accept an unsigned lower module release tag as baseline authority",
			"if verifySignedReleaseTag(ctx, repository, candidate, allowedSignersFile) != nil || semver.Compare(version, currentVersion) >= 0 {",
			"if semver.Compare(version, currentVersion) >= 0 {",
			"TestP8CompatibilityBaselineSelectsGreatestSignedLowerTagAndClosesCompetitors",
		),
		mutation(
			"RELEASE_TRANSITION_SKIPS_CURRENT_EVIDENCE",
			"accept a sole first release without loading its canonical digest-bound compatibility evidence",
			"func compatibilityEvidenceAt(ctx context.Context, repository, modulePrefix, commit string, value compatibility.Manifest) (releaseCompatibilityEvidence, error) {\n\tread := func(path, expected string) ([]byte, error) {",
			"func compatibilityEvidenceAt(ctx context.Context, repository, modulePrefix, commit string, value compatibility.Manifest) (releaseCompatibilityEvidence, error) {\n\treturn releaseCompatibilityEvidence{}, nil\n\tread := func(path, expected string) ([]byte, error) {",
			"TestP8FirstReleaseStillRequiresDigestBoundCanonicalCurrentEvidence",
		),
		mutation(
			"RELEASE_TRANSITION_IGNORES_CORPUS_DIGEST_BINDING",
			"parse compatibility corpus bytes without binding them to the signed manifest digest",
			"if err != nil || compatibility.Digest(encoded) != expected {",
			"if err != nil {",
			"TestP8SignedCompatibilityTransitionRequiresMajorGuideAndAncestry",
		),
		mutation(
			"RELEASE_TRANSITION_ACCEPTS_DIVERGENT_BASELINE",
			"allow a signed release tag on an unrelated branch to define the compatibility baseline",
			"if err := run(ctx, repository, nil, \"git\", \"merge-base\", \"--is-ancestor\", previousCommit, current.Release.Commit); err != nil {",
			"if err := run(ctx, repository, nil, \"git\", \"merge-base\", \"--is-ancestor\", previousCommit, current.Release.Commit); false && err != nil {",
			"TestP8SignedCompatibilityTransitionRequiresMajorGuideAndAncestry",
		),
		mutation(
			"RELEASE_TRANSITION_BYPASSES_COMPARE_RELEASE",
			"seal a candidate even when the semantic layer assessment requires a larger release or missing operator action",
			"if _, err := compatibility.CompareRelease(previous, current, layers); err != nil {",
			"if _, err := compatibility.CompareRelease(previous, current, layers); false && err != nil {",
			"TestP8SignedCompatibilityTransitionRequiresMajorGuideAndAncestry",
		),
		mutation(
			"RELEASE_TRANSITION_CLI_DIGEST_CLAIMS_UNCHANGED",
			"classify a changed CLI compatibility digest as unchanged",
			"CLIJSON:        conservativeDigestAssessment(previous.Digests.CLIJSON, current.Digests.CLIJSON),",
			"CLIJSON:        compatibility.LayerUnchanged,",
			"TestP8CLIAndObservationClassificationsRouteThroughReleasePolicy",
		),
		mutation(
			"RELEASE_TRANSITION_OBSERVATION_CLAIMS_UNCHANGED",
			"discard the semantic Observation inventory comparison",
			"Observation:    compatibility.CompareObservation(before.observation, after.observation),",
			"Observation:    compatibility.LayerUnchanged,",
			"TestP8CLIAndObservationClassificationsRouteThroughReleasePolicy",
		),
	}
}
