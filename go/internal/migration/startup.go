package migration

import (
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
)

// BundlePhysicalSchema decodes the selected generated physical document and
// verifies both of its fingerprints. It does not inspect or modify a database.
func BundlePhysicalSchema(bundle golem.SchemaBundle, provider golem.Provider) (physical.PhysicalSchema, error) {
	for _, document := range bundle.Providers() {
		if document.Provider() == provider {
			schema := document.Schema()
			return physical.CanonicalDecodeVerified(schema.Bytes(), physical.Digest(schema.Fingerprint()), physical.Digest(document.SystemFingerprint()))
		}
	}
	return physical.PhysicalSchema{}, fmt.Errorf("provider %q has no physical schema", provider)
}

// PrepareReviewedStartup verifies the generation-bound embedded history and
// its exact agreement with the expected model, provider and physical head.
// Live ledger/catalog verification remains with the selected provider.
func PrepareReviewedStartup(bundle golem.SchemaBundle, provider golem.Provider, expected physical.PhysicalSchema) (Manifest, error) {
	manifestBytes, err := bundle.MigrationManifest(provider)
	if err != nil {
		return Manifest{}, fmt.Errorf("provider %s reviewed migration manifest is missing or does not match the generated application", provider)
	}
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		return Manifest{}, fmt.Errorf("decode reviewed migration manifest: %w", err)
	}
	if err := VerifyEmbeddedManifest(manifest); err != nil {
		return Manifest{}, fmt.Errorf("invalid reviewed migration manifest: %w", err)
	}
	if len(manifest.Entries) == 0 {
		return Manifest{}, fmt.Errorf("reviewed migration manifest is empty")
	}
	if string(manifest.Provider.Provider) != string(provider) || !reflect.DeepEqual(manifest.Provider, expected.Provider) {
		return Manifest{}, fmt.Errorf("reviewed migration provider does not match the generated physical schema")
	}
	head := manifest.Entries[len(manifest.Entries)-1]
	fingerprint, err := physical.PhysicalFingerprint(expected)
	if err != nil {
		return Manifest{}, fmt.Errorf("fingerprint generated physical schema: %w", err)
	}
	if head.AfterModel != Digest(bundle.Model().Fingerprint().String()) || head.AfterPhysical != Digest(fingerprint.String()) {
		return Manifest{}, fmt.Errorf("reviewed migration head does not match the generated schema")
	}
	return manifest, nil
}
