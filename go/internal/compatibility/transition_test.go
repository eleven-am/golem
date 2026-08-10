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
	transition, err = CompareRelease(base, major, LayerAssessments{
		PublicGoAPI: LayerBreaking, GeneratedGoABI: LayerUnchanged, GraphQLABI: LayerUnchanged,
		CLIJSON: LayerUnchanged, Observation: LayerUnchanged,
	})
	if err != nil || transition != (Transition{Actual: ReleaseMajor, Required: ReleaseMajor}) {
		t.Fatalf("major transition=%#v err=%v", transition, err)
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
	if transition, err := CompareRelease(base, codec, allLayers(LayerUnchanged)); err == nil || transition.Required != ReleaseMajor {
		t.Fatalf("dropped historical fact codec transition=%#v err=%v", transition, err)
	}
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
