package schema

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestHistoricalBundleRoutesModelV1ThroughFrozenDecoderOnly(t *testing.T) {
	current, _ := testBundle(t)
	if _, err := NewHistorical(current); err != nil {
		t.Fatalf("historical registry rejected exact current ModelIR v2: %v", err)
	}
	currentModel := current.Model()
	v1Payload := bytes.Replace(currentModel.Bytes(), []byte(`"formatVersion":2`), []byte(`"formatVersion":1`), 1)
	v1Fingerprint, err := compilerir.ModelFingerprintV1(v1Payload)
	if err != nil {
		t.Fatal(err)
	}
	v1Document := golem.GeneratedSchemaDocument(1, uint32(compilerir.CanonicalFormatVersion), digest(t, string(v1Fingerprint)), v1Payload)
	historical := golem.GeneratedSchemaBundle(current.GenerationDigest(), current.GeneratorVersion(), current.TemplateABIVersion(), v1Document, current.Contract(), current.Providers()...)

	registry, err := NewHistorical(historical)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Providers()) != 2 {
		t.Fatalf("historical registry providers = %v", registry.Providers())
	}
	if _, err := New(historical); !isSchemaFailure(err, CodeDocument, "model", "unsupported format/canonical versions 1/1") {
		t.Fatalf("active registry accepted historical ModelIR v1: %v", err)
	}
	wrongFingerprint := digest(t, string(v1Fingerprint))
	wrongFingerprint[0] ^= 0xff
	wrongDocument := golem.GeneratedSchemaDocument(1, uint32(compilerir.CanonicalFormatVersion), wrongFingerprint, v1Payload)
	wrongBundle := golem.GeneratedSchemaBundle(current.GenerationDigest(), current.GeneratorVersion(), current.TemplateABIVersion(), wrongDocument, current.Contract(), current.Providers()...)
	if _, err := NewHistorical(wrongBundle); !isSchemaFailure(err, CodeFingerprint, "model", "fingerprint mismatch") {
		t.Fatalf("historical registry accepted wrong original ModelIR-v1 fingerprint: %v", err)
	}

	// A current-only member remains forbidden even when its JSON value is the
	// apparent zero value. The v1 fingerprint is recomputed so this exercises
	// vocabulary closure rather than only outer digest mismatch.
	smuggled := bytes.Replace(v1Payload, []byte(`"equalityIndexes":[]`), []byte(`"equalityIndexes":[],"optimisticConcurrency":null`), 1)
	if _, err := compilerir.ModelFingerprintV1(smuggled); err == nil || !strings.Contains(err.Error(), "optimisticConcurrency") {
		t.Fatalf("frozen ModelIR v1 accepted current-only zero member: %v", err)
	}
}
