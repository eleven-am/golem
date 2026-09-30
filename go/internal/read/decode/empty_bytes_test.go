package decode

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestEmptyBytesStayNonNilThroughDecodeRawValuesAndThePublicRow(t *testing.T) {
	fixture := newMatrixFixture(t)
	field := policyir.FieldID(fixture.fields["Bytes"])
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		decoder, err := NewFields(policyir.ModelID(fixture.model), fixture.registry, provider, []policyir.FieldID{field})
		if err != nil {
			t.Fatal(err)
		}
		scan := decoder.NewScan()
		scan.slots[0].bytes = []byte{}
		if raw, ok := scan.RawValues()[0].([]byte); !ok || raw == nil {
			t.Fatalf("provider %d raw empty bytes=%#v", provider, scan.RawValues()[0])
		}
		cells, err := scan.Decode()
		if err != nil {
			t.Fatal(err)
		}
		if cells[0].IsNull() {
			t.Fatalf("provider %d decoded empty bytes as NULL", provider)
		}
		row, err := golem.RuntimeModelReadRow(golem.ModelID(fixture.model), cells[0].RuntimeCell())
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			value, present := golem.RuntimeTransportField(row, golem.FieldID(field)).Get()
			if data, ok := value.([]byte); !present || !ok || data == nil || len(data) != 0 {
				t.Fatalf("provider %d public empty bytes=%#v present=%t", provider, value, present)
			}
		}
	}
}

func TestBytesSlotDecodesHexProjectedAndRawValues(t *testing.T) {
	for _, test := range []struct {
		source any
		want   []byte
		null   bool
	}{
		{source: "", want: []byte{}},
		{source: "00FF10", want: []byte{0, 255, 16}},
		{source: []byte{}, want: []byte{}},
		{source: []byte{7}, want: []byte{7}},
		{source: nil, null: true},
	} {
		value := &slot{kind: slotBytes}
		if err := value.destination().(interface{ Scan(any) error }).Scan(test.source); err != nil {
			t.Fatalf("scan %#v: %v", test.source, err)
		}
		got, present := value.value()
		data, _ := got.([]byte)
		if present == test.null || !test.null && (data == nil || string(data) != string(test.want)) {
			t.Fatalf("scan %#v = %#v present=%t", test.source, got, present)
		}
	}
	if err := (&slot{kind: slotBytes}).destination().(interface{ Scan(any) error }).Scan("zz"); err == nil {
		t.Fatal("non-hexadecimal projected bytes were accepted")
	}
}
