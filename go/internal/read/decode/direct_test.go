package decode

import (
	"bytes"
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestNewFieldsDecodesOrderedPrivateMutationProjection(t *testing.T) {
	fixture := newMatrixFixture(t)
	ordered := []policyir.FieldID{
		policyir.FieldID(fixture.fields["String"]),
		policyir.FieldID(fixture.fields["Bool"]),
		policyir.FieldID(fixture.fields["Bytes"]),
	}
	decoder, err := NewFields(policyir.ModelID(fixture.model), fixture.registry, policyir.ProviderSQLite, ordered)
	if err != nil {
		t.Fatal(err)
	}
	scan := decoder.NewScan()
	scan.slots[0].text.String, scan.slots[0].text.Valid = "private", true
	scan.slots[1].integer.Int64, scan.slots[1].integer.Valid = 1, true
	scan.slots[2].bytes = []byte{1, 2, 3}
	raw := scan.RawValues()
	if len(raw) != 3 || raw[0] != "private" || raw[1] != int64(1) || !bytes.Equal(raw[2].([]byte), []byte{1, 2, 3}) {
		t.Fatalf("raw values=%#v", raw)
	}
	raw[2].([]byte)[0] = 9
	cells, err := scan.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 3 || cells[0].FieldID() != ordered[0] || cells[1].FieldID() != ordered[1] || cells[2].FieldID() != ordered[2] {
		t.Fatalf("ordered cells=%#v", cells)
	}
	decodedBytes, ok := cells[2].PolicyValue()
	gotBytes, bytesOK := decodedBytes.Bytes()
	if !ok || !bytesOK || !bytes.Equal(gotBytes, []byte{1, 2, 3}) {
		t.Fatalf("decoded bytes=%v policy=%t bytes=%t", gotBytes, ok, bytesOK)
	}
	if cells[0].Public() || cells[1].Public() || cells[2].Public() {
		t.Fatal("internal mutation projection became public")
	}
}

func TestNewFieldsRejectsDuplicateForeignFieldsAndProvider(t *testing.T) {
	fixture := newMatrixFixture(t)
	field := policyir.FieldID(fixture.fields["String"])
	if _, err := NewFields(policyir.ModelID(fixture.model), fixture.registry, policyir.ProviderSQLite, []policyir.FieldID{field, field}); err == nil {
		t.Fatal("duplicate direct field was accepted")
	}
	if _, err := NewFields(policyir.ModelID(fixture.model), fixture.registry, policyir.ProviderSQLite, []policyir.FieldID{{0xff}}); err == nil {
		t.Fatal("foreign direct field was accepted")
	}
	if _, err := NewFields(policyir.ModelID(fixture.model), fixture.registry, 0, []policyir.FieldID{field}); err == nil {
		t.Fatal("unknown provider was accepted")
	}
}
