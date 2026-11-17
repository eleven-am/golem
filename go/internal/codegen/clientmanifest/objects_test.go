package clientmanifest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

func TestDTOManifestPublishesTypedStructureAndVersionWithoutModelIdentity(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/objecttypes", Pattern: "."})
	if compiled.Compilation == nil || len(compiled.Diagnostics) != 0 {
		t.Fatalf("diagnostics=%#v", compiled.Diagnostics)
	}
	document, err := graphqlschema.Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Build(*compiled.Compilation, compiled.ContractFingerprint, document.SDL)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the serialized contract so the test pins the client wire shape.
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["formatVersion"] != float64(2) {
		t.Fatalf("DTO manifest has no version: %s", encoded)
	}
	types, ok := wire["types"].(map[string]any)
	if !ok {
		t.Fatalf("DTO types are absent: %s", encoded)
	}
	page := types["HomeFeedPage"].(map[string]any)
	results := page["fields"].(map[string]any)["results"].(map[string]any)
	if results["kind"] != "list" || results["maxItems"] != float64(25) || results["element"].(map[string]any)["name"] != "HomeSegment" {
		t.Fatalf("result type=%#v", results)
	}
	root := wire["roots"].(map[string]any)["homeFeed"].(map[string]any)
	if root["shape"] != "object" || root["result"].(map[string]any)["name"] != "HomeFeedPage" {
		t.Fatalf("root=%#v", root)
	}
	if !reflect.DeepEqual(root["models"], []any{}) {
		t.Fatalf("DTO acquired model identity: %#v", root)
	}
	metadata := types["TransportMetadata"].(map[string]any)
	if metadata["kind"] != "object" || metadata["fields"].(map[string]any)["_version"].(map[string]any)["name"] != "BigInt" {
		t.Fatalf("ordinary DTO metadata fields=%#v", metadata)
	}
	if types["HomeInput"].(map[string]any)["kind"] != "inputObject" {
		t.Fatal("nested input definition was lost")
	}
	if target := manifest.Types["PlaybackTarget"]; !target.OneOf || len(target.Fields) != 5 {
		t.Fatalf("one-of input metadata=%#v", target)
	}
	if _, exists := manifest.Models["TransportMetadata"]; exists {
		t.Fatal("DTO registered as a model")
	}
	if result := manifest.Models["User"].ComputedResults["metadata"]; result.Kind != ir.GraphQLTypeObject || result.Name != "TransportMetadata" {
		t.Fatalf("computed DTO result shape was lost: %#v", result)
	}
	clientSource, err := Emit(*compiled.Compilation, compiled.ContractFingerprint, document.SDL)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{
		"../../../../client/core/test/fixtures/objects.manifest.ts":    clientSource,
		"../../../../client/core/test/fixtures/objects.schema.graphql": []byte(document.SDL),
	} {
		if os.Getenv("GOLEM_UPDATE_OBJECTS_MANIFEST_FIXTURE") == "1" && path == "../../../../client/core/test/fixtures/objects.manifest.ts" {
			if err := os.WriteFile(path, want, 0644); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, want) {
			t.Fatalf("client DTO fixture %s differs from the Go declarations; regenerate it with the manifest/schema emitters", path)
		}
	}
}

func TestManifestsWithoutDTOsRetainTheirExistingFormat(t *testing.T) {
	manifest, err := Build(shapeCompilation(), ir.Fingerprint("contract"), shapeSDL)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire["formatVersion"]; exists {
		t.Fatal("an existing manifest unexpectedly required a new client")
	}
	if _, exists := wire["types"]; exists {
		t.Fatal("an existing manifest gained an empty type registry")
	}
}
