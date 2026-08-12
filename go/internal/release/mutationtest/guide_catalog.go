package mutationtest

import (
	"time"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

// GuideCatalog is intentionally separate from the already-reviewed release
// transition catalog so its mutation run cannot accidentally repeat or blur
// the earlier eight-mutant evidence.
func GuideCatalog() []p8mutation.Mutation {
	gate := func(test string) p8mutation.Gate {
		return p8mutation.Gate{Directory: "go", Package: "./internal/release", Test: test}
	}
	mutation := func(label, summary, before, after, test string) p8mutation.Mutation {
		return p8mutation.Mutation{
			Label: label, Summary: summary,
			Patches: []p8mutation.Patch{{Path: sourceRelease, Before: before, After: after}},
			Gate:    gate(test), Timeout: 4 * time.Minute,
		}
	}
	return []p8mutation.Mutation{
		mutation(
			"RELEASE_GUIDE_ACCEPTS_PRIOR_BOUND_FIRST_RELEASE",
			"allow a sole release to claim a migration guide whose prior tag does not exist",
			"if current.MigrationGuide != nil || containsString(current.RequiredActions, \"migration-guide.execute\") {",
			"if false && (current.MigrationGuide != nil || containsString(current.RequiredActions, \"migration-guide.execute\")) {",
			"TestP8MigrationGuideTransitionRejectsFirstReleaseAndEverySignedAuthorityTamper",
		),
		mutation(
			"RELEASE_GUIDE_TRUSTS_SELF_COMPUTED_DIGEST",
			"parse signed guide bytes against their self-computed digest instead of the manifest authority",
			"guide, err := compatibility.ParseMigrationGuide(encoded, authority.SHA256)",
			"guide, err := compatibility.ParseMigrationGuide(encoded, compatibility.MigrationGuideDigest(encoded))",
			"TestP8MigrationGuideTransitionRejectsFirstReleaseAndEverySignedAuthorityTamper",
		),
		mutation(
			"RELEASE_GUIDE_BYPASSES_ENDPOINT_AND_ACTION_BINDING",
			"accept a guide whose prior endpoint or required actions disagree with the release transition",
			"if err != nil || compatibility.ValidateMigrationGuideTransition(guide, authority, previousTag, previousCommit, current.Release.Tag, current.Release.Version, current.RequiredActions) != nil {",
			"if err != nil {",
			"TestP8MigrationGuideTransitionRejectsFirstReleaseAndEverySignedAuthorityTamper",
		),
		mutation(
			"RELEASE_GUIDE_SKIPS_CURRENT_CORPUS_TREE",
			"verify retained guide corpora only at the prior signed commit",
			"for _, commit := range []string{previousCommit, current.Release.Commit} {",
			"for _, commit := range []string{previousCommit} {",
			"TestP8MigrationGuideTransitionRejectsFirstReleaseAndEverySignedAuthorityTamper",
		),
		mutation(
			"RELEASE_GUIDE_BUILD_OMITS_ARTIFACT",
			"omit the exact signed migration guide from the staged release output",
			"if err := writeCandidateMigrationGuide(config.OutputDir, config.Candidate); err != nil {",
			"if err := error(nil); err != nil {",
			"TestP8ReleaseArtifactReproducibility",
		),
		mutation(
			"RELEASE_GUIDE_PUBLISH_SKIPS_ARTIFACT_BINDING",
			"publish a staged release without checking its guide bytes and safe path",
			"if err := verifyBuiltMigrationGuide(staged, candidate); err != nil {",
			"if err := error(nil); err != nil {",
			"TestP8ReleaseArtifactReproducibility",
		),
	}
}
