package golem

import (
	"encoding/hex"
	"fmt"
)

// SchemaBundleFormatVersion versions the public, representation-opaque schema
// bundle ABI emitted into the application package.
const SchemaBundleFormatVersion uint16 = 1

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
}

func GeneratedProviderSchemaDocument(provider Provider, systemFingerprint SchemaDigest, schema SchemaDocument) ProviderSchemaDocument {
	return ProviderSchemaDocument{provider: provider, systemFingerprint: systemFingerprint, schema: schema.clone()}
}

func (document ProviderSchemaDocument) Provider() Provider { return document.provider }
func (document ProviderSchemaDocument) SystemFingerprint() SchemaDigest {
	return document.systemFingerprint
}
func (document ProviderSchemaDocument) Schema() SchemaDocument { return document.schema.clone() }
func (document ProviderSchemaDocument) clone() ProviderSchemaDocument {
	return GeneratedProviderSchemaDocument(document.provider, document.systemFingerprint, document.schema)
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
