package golem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// SchemaBundleFormatVersion versions the public, representation-opaque schema
// bundle ABI emitted into the application package.
const SchemaBundleFormatVersion uint16 = 2

// SchemaDigest is a fixed-width SHA-256 digest. Generated code uses array
// literals so malformed or non-canonical textual fingerprints cannot enter the
// runtime registry.
type SchemaDigest [32]byte

func (digest SchemaDigest) String() string { return hex.EncodeToString(digest[:]) }

// GenerationDigestError reports a mixed, missing, or invalid package
// generation during explicit application-registry composition.
type GenerationDigestError struct {
	Registry     string
	PackageIndex int
	Expected     SchemaDigest
	Actual       SchemaDigest
}

func (err *GenerationDigestError) Error() string {
	if err.PackageIndex < 0 {
		return fmt.Sprintf("generated %s registry has an unstamped expected generation digest", err.Registry)
	}
	return fmt.Sprintf("generated %s package %d has generation digest %s; want %s", err.Registry, err.PackageIndex, err.Actual, err.Expected)
}

func validateGenerationDigests(registry string, expected SchemaDigest, actual []SchemaDigest) error {
	if expected == (SchemaDigest{}) {
		return &GenerationDigestError{Registry: registry, PackageIndex: -1}
	}
	for index, digest := range actual {
		if digest != expected {
			return &GenerationDigestError{Registry: registry, PackageIndex: index, Expected: expected, Actual: digest}
		}
	}
	return nil
}

// SchemaDocument is one immutable canonical schema blob and its exact format
// and fingerprint identity. Its representation is intentionally opaque: a
// future internal runtime consumer owns decoding and validation semantics.
type SchemaDocument struct {
	formatVersion    uint32
	canonicalVersion uint32
	fingerprint      SchemaDigest
	payload          []byte
}

func GeneratedSchemaDocument(formatVersion, canonicalVersion uint32, fingerprint SchemaDigest, payload []byte) SchemaDocument {
	return SchemaDocument{formatVersion: formatVersion, canonicalVersion: canonicalVersion, fingerprint: fingerprint, payload: append([]byte(nil), payload...)}
}

func (document SchemaDocument) FormatVersion() uint32     { return document.formatVersion }
func (document SchemaDocument) CanonicalVersion() uint32  { return document.canonicalVersion }
func (document SchemaDocument) Fingerprint() SchemaDigest { return document.fingerprint }
func (document SchemaDocument) Bytes() []byte             { return append([]byte(nil), document.payload...) }
func (document SchemaDocument) clone() SchemaDocument {
	return GeneratedSchemaDocument(document.formatVersion, document.canonicalVersion, document.fingerprint, document.payload)
}

// ProviderSchemaDocument associates a canonical PhysicalSchema blob with its
// declared provider. Physical identifiers exist only inside SchemaDocument's
// opaque byte payload.
type ProviderSchemaDocument struct {
	provider          Provider
	systemFingerprint SchemaDigest
	schema            SchemaDocument
	migration         MigrationManifestDocument
	hasMigration      bool
}

func GeneratedProviderSchemaDocument(provider Provider, systemFingerprint SchemaDigest, schema SchemaDocument) ProviderSchemaDocument {
	return ProviderSchemaDocument{provider: provider, systemFingerprint: systemFingerprint, schema: schema.clone()}
}

// GeneratedProviderSchemaDocumentWithMigration is the generated-code-only
// composition point for one provider schema and its exact reviewed migration
// history. Applications receive the resulting opaque value; migration
// implementation types never cross the public package boundary.
func GeneratedProviderSchemaDocumentWithMigration(provider Provider, systemFingerprint SchemaDigest, schema SchemaDocument, migration MigrationManifestDocument) ProviderSchemaDocument {
	return ProviderSchemaDocument{provider: provider, systemFingerprint: systemFingerprint, schema: schema.clone(), migration: migration.clone(), hasMigration: true}
}

func (document ProviderSchemaDocument) Provider() Provider { return document.provider }
func (document ProviderSchemaDocument) SystemFingerprint() SchemaDigest {
	return document.systemFingerprint
}
func (document ProviderSchemaDocument) Schema() SchemaDocument { return document.schema.clone() }
func (document ProviderSchemaDocument) clone() ProviderSchemaDocument {
	if document.hasMigration {
		return GeneratedProviderSchemaDocumentWithMigration(document.provider, document.systemFingerprint, document.schema, document.migration)
	}
	return GeneratedProviderSchemaDocument(document.provider, document.systemFingerprint, document.schema)
}

// MigrationManifestDocument is an immutable, representation-opaque reviewed
// migration manifest embedded into generated applications. Its binding digest
// prevents a manifest from another generation or provider being substituted
// without detection during runtime preflight.
type MigrationManifestDocument struct {
	formatVersion    uint16
	generationDigest SchemaDigest
	provider         Provider
	bindingDigest    SchemaDigest
	payload          []byte
}

const migrationManifestDocumentFormatVersion uint16 = 1

func GeneratedMigrationManifestDocument(generationDigest SchemaDigest, provider Provider, payload []byte) MigrationManifestDocument {
	return MigrationManifestDocument{
		formatVersion: migrationManifestDocumentFormatVersion, generationDigest: generationDigest,
		provider: provider, bindingDigest: migrationManifestBinding(generationDigest, provider, payload),
		payload: append([]byte(nil), payload...),
	}
}

func (document MigrationManifestDocument) clone() MigrationManifestDocument {
	return MigrationManifestDocument{
		formatVersion: document.formatVersion, generationDigest: document.generationDigest,
		provider: document.provider, bindingDigest: document.bindingDigest,
		payload: append([]byte(nil), document.payload...),
	}
}

func validateMigrationManifestDocument(document MigrationManifestDocument, generationDigest SchemaDigest, provider Provider) ([]byte, error) {
	if document.formatVersion != migrationManifestDocumentFormatVersion || document.generationDigest == (SchemaDigest{}) || document.provider == "" ||
		document.generationDigest != generationDigest || document.provider != provider ||
		document.bindingDigest != migrationManifestBinding(document.generationDigest, document.provider, document.payload) {
		return nil, fmt.Errorf("generated migration manifest identity is invalid")
	}
	return append([]byte(nil), document.payload...), nil
}

func migrationManifestBinding(generationDigest SchemaDigest, provider Provider, payload []byte) SchemaDigest {
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem-reviewed-migration-manifest-v1\x00"))
	_, _ = hash.Write(generationDigest[:])
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(provider))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	var result SchemaDigest
	copy(result[:], hash.Sum(nil))
	return result
}

// SchemaBundle is the immutable deployable schema registry embedded in the
// generated application binary. It contains canonical logical, contract, and
// provider documents without exposing compiler or physical-schema types.
type SchemaBundle struct {
	formatVersion      uint16
	generationDigest   SchemaDigest
	generatorVersion   string
	templateABIVersion string
	model              SchemaDocument
	contract           SchemaDocument
	providers          []ProviderSchemaDocument
}

func GeneratedSchemaBundle(generationDigest SchemaDigest, generatorVersion, templateABIVersion string, model, contract SchemaDocument, providers ...ProviderSchemaDocument) SchemaBundle {
	bundle := SchemaBundle{
		formatVersion: SchemaBundleFormatVersion, generationDigest: generationDigest,
		generatorVersion: generatorVersion, templateABIVersion: templateABIVersion,
		model: model.clone(), contract: contract.clone(),
		providers: make([]ProviderSchemaDocument, len(providers)),
	}
	for index, provider := range providers {
		bundle.providers[index] = provider.clone()
	}
	return bundle
}

func (bundle SchemaBundle) FormatVersion() uint16          { return bundle.formatVersion }
func (bundle SchemaBundle) GenerationDigest() SchemaDigest { return bundle.generationDigest }
func (bundle SchemaBundle) GeneratorVersion() string       { return bundle.generatorVersion }
func (bundle SchemaBundle) TemplateABIVersion() string     { return bundle.templateABIVersion }
func (bundle SchemaBundle) Model() SchemaDocument          { return bundle.model.clone() }
func (bundle SchemaBundle) Contract() SchemaDocument       { return bundle.contract.clone() }
func (bundle SchemaBundle) Providers() []ProviderSchemaDocument {
	result := make([]ProviderSchemaDocument, len(bundle.providers))
	for index, provider := range bundle.providers {
		result[index] = provider.clone()
	}
	return result
}

// MigrationManifest returns the selected provider's generation-bound reviewed
// manifest as copy-isolated canonical bytes. The document representation and
// binding details remain private to the bundle.
func (bundle SchemaBundle) MigrationManifest(provider Provider) ([]byte, error) {
	for _, document := range bundle.providers {
		if document.provider != provider {
			continue
		}
		if !document.hasMigration {
			return nil, fmt.Errorf("generated provider has no reviewed migration manifest")
		}
		return validateMigrationManifestDocument(document.migration, bundle.generationDigest, provider)
	}
	return nil, fmt.Errorf("generated provider is absent")
}
