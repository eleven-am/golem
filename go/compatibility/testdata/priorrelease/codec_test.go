package priorrelease

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

// This package imports the released module, never the checkout. Its module path
// remains inside go/'s internal-import boundary without replacing that module.
func TestReleasedCodec(t *testing.T) {
	dir := os.Getenv("GOLEM_COMPAT_FIXTURES")
	if dir == "" {
		t.Fatal("run through the parent compatibility test")
	}
	fixture := schematest.NewSubscribedGraph(t)
	model, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, _ := model.EventSchema()
	digest, err := fact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		t.Helper()
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || info.Size() > 64<<10 {
			t.Fatalf("invalid bounded fixture %s: %v", name, err)
		}
		value, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	v3 := read("current-v3.bin")
	decoded, err := fact.Decode(v3, fixture.Registry, digest)
	if err != nil || decoded.FormatVersion() != 3 {
		t.Fatalf("v0.8.8 refused compatible V3: %v", err)
	}
	encoded, err := fact.Encode(decoded)
	if err != nil || !bytes.Equal(v3, encoded) {
		t.Fatalf("v0.8.8 changed exact V3 bytes: %v", err)
	}
	// Build a new envelope through the released constructor so these bytes are
	// authored by v0.8.8, rather than merely echoing the current encoder's input.
	released, err := releasedFact(fixture.Registry, fixture.Post, fixture.PostID, digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fact.Decode(released, fixture.Registry, digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "released-v3.bin"), released, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = fact.Decode(read("current-v4.bin"), fixture.Registry, digest)
	var unsupported *fact.CodecError
	if !errors.As(err, &unsupported) || unsupported.Detail != "unsupported fact version 4" {
		t.Fatalf("v0.8.8 must reject V4 at format admission, got %v", err)
	}
}
