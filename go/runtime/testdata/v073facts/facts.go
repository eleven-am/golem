package v073facts

import (
	"encoding/hex"
	"time"

	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

func decode(value string) []byte {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		panic(err)
	}
	if len(decoded) == 0 {
		return nil
	}
	return decoded
}

func UnchangedEventSchemaV2() mutationfact.OutboxRow {
	return mutationfact.OutboxRow{
		EventID: "d3fbf98e-af51-41d7-a7dd-013eba36b3c6", FactVersion: 2, CodecIdentity: "golem.fact.v2",
		GenerationFingerprint: "31b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f", ModelID: "3dc9f4fb15ff37ebad4c00faa1a41895", Action: "created",
		BeforeIdentity: decode(""), AfterIdentity: decode("474f4c454d49440001cf2f9c0f16fe3eb2330b21e3f1a7d0d900000001c1c8977c0136d3d3a6636906ce9eedb0010a00000000000000000000000000000001"),
		CausationID: "b5008ae3-2558-44e4-882e-e676b40fe359", TransactionOrdinal: 1,
		Metadata: decode("474f4c454d4641435400020000000d676f6c656d2e666163742e7632d3fbf98eaf5141d7a7dd013eba36b3c631b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f663cfba039e8bdc28fd246c784aa5b4b02a86c3cf8beb6b6fc72d14e52ca459e3dc9f4fb15ff37ebad4c00faa1a4189501b5008ae3255844e4882ee676b40fe359000000010001cf2f9c0f16fe3eb2330b21e3f1a7d0d900000001c1c8977c0136d3d3a6636906ce9eedb0010a000000000000000000000000000000010000000000"), DeleteSnapshot: decode(""),
		RecordedAt: time.UnixMicro(1793651492605528).UTC(),
	}
}

func ChangedEventSchemaV2() mutationfact.OutboxRow {
	return mutationfact.OutboxRow{
		EventID: "d3fbf98e-af51-41d7-a7dd-013eba36b3c6", FactVersion: 2, CodecIdentity: "golem.fact.v2",
		GenerationFingerprint: "31b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f", ModelID: "3dc9f4fb15ff37ebad4c00faa1a41895", Action: "created",
		BeforeIdentity: decode(""), AfterIdentity: decode("474f4c454d49440001cf2f9c0f16fe3eb2330b21e3f1a7d0d900000001c1c8977c0136d3d3a6636906ce9eedb0010a00000000000000000000000000000001"),
		CausationID: "b5008ae3-2558-44e4-882e-e676b40fe359", TransactionOrdinal: 1,
		Metadata: decode("474f4c454d4641435400020000000d676f6c656d2e666163742e7632d3fbf98eaf5141d7a7dd013eba36b3c631b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f993cfba039e8bdc28fd246c784aa5b4b02a86c3cf8beb6b6fc72d14e52ca459e3dc9f4fb15ff37ebad4c00faa1a4189501b5008ae3255844e4882ee676b40fe359000000010001cf2f9c0f16fe3eb2330b21e3f1a7d0d900000001c1c8977c0136d3d3a6636906ce9eedb0010a000000000000000000000000000000010000000000"), DeleteSnapshot: decode(""),
		RecordedAt: time.UnixMicro(1793651492605528).UTC(),
	}
}

func GenerationBoundV1() mutationfact.OutboxRow {
	return mutationfact.OutboxRow{
		EventID: "00000000-0000-0000-0000-00000000005b", FactVersion: 1, CodecIdentity: "golem.fact.v1",
		GenerationFingerprint: "31b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f", ModelID: "3dc9f4fb15ff37ebad4c00faa1a41895", Action: "created",
		BeforeIdentity: decode(""), AfterIdentity: decode("474f4c454d49440001cf2f9c0f16fe3eb2330b21e3f1a7d0d900000001c1c8977c0136d3d3a6636906ce9eedb0010a0000000000000000000000000000004d"),
		CausationID: "00000000-0000-0000-0000-00000000005c", TransactionOrdinal: 1,
		Metadata: decode("474f4c454d4641435400010000000d676f6c656d2e666163742e76310000000000000000000000000000005b31b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f3dc9f4fb15ff37ebad4c00faa1a41895010000000000000000000000000000005c000000010001cf2f9c0f16fe3eb2330b21e3f1a7d0d900000001c1c8977c0136d3d3a6636906ce9eedb0010a0000000000000000000000000000004d00000000"), DeleteSnapshot: decode(""),
		RecordedAt: time.UnixMicro(1700000000000000).UTC(),
	}
}

const (
	UnchangedEventSchemaDigest = "663cfba039e8bdc28fd246c784aa5b4b02a86c3cf8beb6b6fc72d14e52ca459e"
	ChangedEventSchemaDigest   = "993cfba039e8bdc28fd246c784aa5b4b02a86c3cf8beb6b6fc72d14e52ca459e"
	GenerationDigest           = "31b6ee4b455d5b66037852eefd38bd8965bb6905ddf162ad6c9d7b1bcba4db4f"
)
