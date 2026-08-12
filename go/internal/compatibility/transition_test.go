package compatibility

import (
	"strings"
	"testing"
)

func TestCompatibilitySemanticVersionContract(t *testing.T) {
	base := publishedCompatibilityFixture("v1.2.3", '1')
	unchanged := allLayers(LayerUnchanged)

	patch := publishedCompatibilityFixture("v1.2.4", '2')
	transition, err := CompareRelease(base, patch, unchanged)
	if err != nil || transition != (Transition{Actual: ReleasePatch, Required: ReleasePatch}) {
		t.Fatalf("patch transition=%#v err=%v", transition, err)
	}

	minor := publishedCompatibilityFixture("v1.3.0", '3')
	minor.Digests.PublicGoAPI = strings.Repeat("a", 64)
	transition, err = CompareRelease(base, minor, LayerAssessments{
		PublicGoAPI: LayerAdditive, GeneratedGoABI: LayerUnchanged, GraphQLABI: LayerUnchanged,
		CLIJSON: LayerUnchanged, Observation: LayerUnchanged,
	})
	if err != nil || transition != (Transition{Actual: ReleaseMinor, Required: ReleaseMinor}) {
		t.Fatalf("minor transition=%#v err=%v", transition, err)
	}

	insufficient := publishedCompatibilityFixture("v1.2.4", '4')
	insufficient.Digests.PublicGoAPI = strings.Repeat("b", 64)
	transition, err = CompareRelease(base, insufficient, LayerAssessments{
		PublicGoAPI: LayerBreaking, GeneratedGoABI: LayerUnchanged, GraphQLABI: LayerUnchanged,
		CLIJSON: LayerUnchanged, Observation: LayerUnchanged,
	})
	if err == nil || transition.Required != ReleaseMajor || transition.Actual != ReleasePatch {
		t.Fatalf("insufficient transition=%#v err=%v", transition, err)
	}

	major := publishedCompatibilityFixture("v2.0.0", '5')
	major.Digests.PublicGoAPI = strings.Repeat("c", 64)
	major.RequiredActions = []string{"migration-guide.execute"}
	major.MigrationGuide = testMigrationGuideAuthority("go/v1.2.3", "v2.0.0", '6')
	transition, err = CompareRelease(base, major, LayerAssessments{
		PublicGoAPI: LayerBreaking, GeneratedGoABI: LayerUnchanged, GraphQLABI: LayerUnchanged,
		CLIJSON: LayerUnchanged, Observation: LayerUnchanged,
	})
	if err != nil || transition != (Transition{Actual: ReleaseMajor, Required: ReleaseMajor}) {
		t.Fatalf("major transition=%#v err=%v", transition, err)
	}
}

func TestGeneratedGoBreakingTransitionRequiresPreStableMinorAndMigrationGuide(t *testing.T) {
	base := publishedCompatibilityFixture("v0.0.2", '1')
	layers := allLayers(LayerUnchanged)
	layers.GeneratedGoABI = LayerBreaking

	patch := publishedCompatibilityFixture("v0.0.3", '2')
	patch.Digests.GeneratedGoABI = strings.Repeat("a", 64)
	patch.RequiredActions = []string{"migration-guide.execute", "regenerate.generated"}
	patch.MigrationGuide = testMigrationGuideAuthority("go/v0.0.2", "v0.1.0", '6')
	transition, err := CompareRelease(base, patch, layers)
	if err == nil || transition.Actual != ReleasePatch || transition.Required != ReleaseMajor {
		t.Fatalf("breaking generated patch transition=%#v err=%v", transition, err)
	}

	minor := publishedCompatibilityFixture("v0.1.0", '2')
	minor.Digests.GeneratedGoABI = strings.Repeat("b", 64)
	minor.RequiredActions = []string{"regenerate.generated"}
	transition, err = CompareRelease(base, minor, layers)
	if err == nil || transition.Actual != ReleaseMinor || transition.Required != ReleaseMinor {
		t.Fatalf("breaking generated minor transition=%#v err=%v", transition, err)
	}

	minor.RequiredActions = []string{"migration-guide.execute", "regenerate.generated"}
	minor.MigrationGuide = testMigrationGuideAuthority("go/v0.0.2", "v0.1.0", '7')
	transition, err = CompareRelease(base, minor, layers)
	if err != nil || transition != (Transition{Actual: ReleaseMinor, Required: ReleaseMinor}) {
		t.Fatalf("reviewed breaking generated pre-stable minor transition=%#v err=%v", transition, err)
	}
}

func TestCompatibilityTransitionsRequireActionsAndHistoricalDecoders(t *testing.T) {
	base := publishedCompatibilityFixture("v1.2.3", '1')
	changed := publishedCompatibilityFixture("v1.3.0", '2')
	changed.Versions.GeneratedTemplateABI = "p8-go-abi-v6"
	changed.Digests.GeneratedGoABI = strings.Repeat("a", 64)
	layers := LayerAssessments{
		PublicGoAPI: LayerUnchanged, GeneratedGoABI: LayerAdditive, GraphQLABI: LayerUnchanged,
		CLIJSON: LayerUnchanged, Observation: LayerUnchanged,
	}
	if _, err := CompareRelease(base, changed, layers); err == nil {
		t.Fatal("generated ABI transition without regeneration action accepted")
	}
	changed.RequiredActions = []string{"regenerate.generated"}
	if transition, err := CompareRelease(base, changed, layers); err != nil || transition.Required != ReleaseMinor {
		t.Fatalf("generated ABI transition=%#v err=%v", transition, err)
	}

	codec := publishedCompatibilityFixture("v1.3.0", '3')
	codec.Versions.FactCodecs = []string{"golem.fact.v2", "golem.fact.v3"}
	codec.HistoricalDecode.FactCodecs = []string{"golem.fact.v2", "golem.fact.v3"}
	codec.RequiredActions = []string{"migration-guide.execute"}
	codec.MigrationGuide = testMigrationGuideAuthority("go/v1.2.3", "v2.0.0", '8')
	if transition, err := CompareRelease(base, codec, allLayers(LayerUnchanged)); err == nil || transition.Required != ReleaseMajor {
		t.Fatalf("dropped historical fact codec transition=%#v err=%v", transition, err)
	}
}

func testMigrationGuideAuthority(fromTag, toVersion string, digest byte) *MigrationGuideAuthority {
	return &MigrationGuideAuthority{Path: "compatibility/migration-guide.json", SHA256: strings.Repeat(string(digest), 64), FromTag: fromTag, ToVersion: toVersion}
}

func TestCompatibilityProviderAndDeploymentProfilesRemainDistinct(t *testing.T) {
	missingC := publishedCompatibilityFixture("v1.3.0", '2')
	missingC.Providers[0].VerificationProfiles = []string{"linguistic"}
	if _, err := Encode(missingC); err == nil {
		t.Fatal("PostgreSQL manifest without C verification profile encoded")
	}
	confused := publishedCompatibilityFixture("v1.3.0", '3')
	confused.DeploymentProfiles = []string{"adapted-multi-process", "database-backed-single-process", "embedded-single-process", "linguistic"}
	if _, err := Encode(confused); err == nil {
		t.Fatal("collation verification profile accepted as deployment topology")
	}

	addProfile := publishedCompatibilityFixture("v1.3.0", '4')
	addProfile.Providers[0].VerificationProfiles = []string{"c", "linguistic", "provider-15-next"}
	if _, err := Encode(addProfile); err == nil {
		t.Fatal("undeclared provider verification profile encoded without a manifest format change")
	}
}

func publishedCompatibilityFixture(version string, commit byte) Manifest {
	value := compatibilityFixture()
	value.Release = Release{
		Version: version, Tag: "go/" + version,
		Commit: strings.Repeat(string(commit), 40),
	}
	return value
}

func allLayers(value LayerChange) LayerAssessments {
	return LayerAssessments{
		PublicGoAPI: value, GeneratedGoABI: value, GraphQLABI: value,
		CLIJSON: value, Observation: value,
	}
}
