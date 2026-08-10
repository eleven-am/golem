package physical

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	physicalFingerprintDomain           = "golem:physical-schema:v1"
	systemFingerprintDomain             = "golem:system-schema:v1"
	unmanagedAllowlistFingerprintDomain = "golem:unmanaged-allowlist:v1"
)

type Digest [sha256.Size]byte

func (digest Digest) String() string { return hex.EncodeToString(digest[:]) }

func (digest Digest) MarshalText() ([]byte, error) { return []byte(digest.String()), nil }

// PhysicalFingerprint hashes application physical state and the reviewed
// unmanaged allowlist. System objects are deliberately excluded and use the
// separately domain-separated SystemFingerprint.
func PhysicalFingerprint(schema PhysicalSchema) (Digest, error) {
	normalized, err := Normalize(schema)
	if err != nil {
		return Digest{}, err
	}
	normalized.System = SystemSchema{}
	encoded, err := canonicalValue(normalized)
	if err != nil {
		return Digest{}, err
	}
	return fingerprint(physicalFingerprintDomain, encoded), nil
}

// UnmanagedAllowlistFingerprint hashes the normalized reviewed allowlist in
// provider and namespace scope. It is separate from PhysicalFingerprint so a
// manifest can bind its explicit allowlist field to the embedded head snapshot.
func UnmanagedAllowlistFingerprint(schema PhysicalSchema) (Digest, error) {
	normalized, err := Normalize(schema)
	if err != nil {
		return Digest{}, err
	}
	value := struct {
		Provider  ProviderManifest
		Namespace Namespace
		Unmanaged []UnmanagedObject
	}{Provider: normalized.Provider, Namespace: normalized.Namespace, Unmanaged: normalized.Unmanaged}
	encoded, err := canonicalValue(value)
	if err != nil {
		return Digest{}, err
	}
	return fingerprint(unmanagedAllowlistFingerprintDomain, encoded), nil
}

// SystemFingerprint validates and hashes the provider manifest plus its
// versioned system schema in a domain that cannot collide with application
// objects.
func SystemFingerprint(provider ProviderManifest, system SystemSchema) (Digest, error) {
	namespace := PhysicalName("public")
	if provider.Provider == ir.SQLite {
		namespace = "main"
	}
	probe := PhysicalSchema{
		Version:          SchemaFormatVersion,
		CanonicalVersion: CanonicalFormatVersion,
		Provider:         provider,
		Namespace:        Namespace{Name: namespace},
		System:           system,
	}
	normalized, err := Normalize(probe)
	if err != nil {
		return Digest{}, err
	}
	value := struct {
		Provider ProviderManifest
		System   SystemSchema
	}{Provider: normalized.Provider, System: normalized.System}
	encoded, err := canonicalValue(value)
	if err != nil {
		return Digest{}, err
	}
	return fingerprint(systemFingerprintDomain, encoded), nil
}

func fingerprint(domain string, encoded []byte) Digest {
	hash := sha256.New()
	writeStringHash(hash, domain)
	writeBytesHash(hash, encoded)
	var result Digest
	copy(result[:], hash.Sum(nil))
	return result
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeStringHash(writer hashWriter, value string) { writeBytesHash(writer, []byte(value)) }

func writeBytesHash(writer hashWriter, value []byte) {
	var length [10]byte
	encoded := putUvarint(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:encoded])
	_, _ = writer.Write(value)
}

func putUvarint(buffer []byte, value uint64) int {
	index := 0
	for value >= 0x80 {
		buffer[index] = byte(value) | 0x80
		value >>= 7
		index++
	}
	buffer[index] = byte(value)
	return index + 1
}

func ParseDigest(value string) (Digest, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return Digest{}, fmt.Errorf("physical fingerprint %q is not a canonical SHA-256 digest", value)
	}
	if hex.EncodeToString(decoded) != value {
		return Digest{}, fmt.Errorf("physical fingerprint %q is not lowercase canonical hexadecimal", value)
	}
	var result Digest
	copy(result[:], decoded)
	return result, nil
}
