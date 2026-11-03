package fact

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

const (
	frozenV1Metadata = "474f4c454d4641435400010000000d676f6c656d2e666163742e76310000000000000000000000000000005b31b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f3dc9f4fb15ff37ebad4c00faa1a41895010000000000000000000000000000005c000000010001cf2f9c0f16fe3eb2330b21e3f1a7d0d900000001c1c8977c0136d3d3a6636906ce9eedb0010a0000000000000000000000000000004d00000000"
	frozenV2Metadata = "474f4c454d4641435400020000000d676f6c656d2e666163742e7632d3fbf98eaf5141d7a7dd013eba36b3c631b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f663cfba039e8bdc28fd246c784aa5b4b02a86c3cf8beb6b6fc72d14e52ca459e3dc9f4fb15ff37ebad4c00faa1a4189501b5008ae3255844e4882ee676b40fe359000000010001cf2f9c0f16fe3eb2330b21e3f1a7d0d900000001c1c8977c0136d3d3a6636906ce9eedb0010a000000000000000000000000000000010000000000"
)

func TestRetiredFactFormatsAreRefusedTyped(t *testing.T) {
	registry := schematest.NewIndexedExact(t).Registry
	resolver := testHistoricalResolver{registry: registry, digest: testEventSchema, enabled: true}
	for name, test := range map[string]struct {
		metadata string
		version  uint16
		codec    string
		message  string
	}{
		"v1": {frozenV1Metadata, 1, "golem.fact.v1", "GOLEM_EVENT_FORMAT_RETIRED: stored event fact (format V1) is no longer readable: drain undelivered events on go/v0.8.0 before upgrading"},
		"v2": {frozenV2Metadata, 2, "golem.fact.v2", "GOLEM_EVENT_FORMAT_RETIRED: stored event fact (format V2) is no longer readable: drain undelivered events on go/v0.8.0 before upgrading"},
	} {
		t.Run(name, func(t *testing.T) {
			payload, err := hex.DecodeString(test.metadata)
			if err != nil {
				t.Fatal(err)
			}
			for entry, decode := range map[string]func() error{
				"Decode":       func() error { _, err := Decode(payload, registry, testEventSchema); return err },
				"DecodeOutbox": func() error { _, err := DecodeOutbox(payload, nil, registry, testEventSchema); return err },
				"Resolver":     func() error { _, err := DecodeWithResolver(payload, resolver); return err },
				"StoredRow": func() error {
					_, err := ValidateStoredRow(OutboxRow{FactVersion: int64(test.version), CodecIdentity: test.codec, Metadata: payload}, resolver)
					return err
				},
			} {
				err := decode()
				var retired *FormatRetiredError
				if !errors.As(err, &retired) || retired.Version() != test.version || err.Error() != test.message {
					t.Fatalf("%s: error = %v, want retired V%d %q", entry, err, test.version, test.message)
				}
			}
		})
	}
}
