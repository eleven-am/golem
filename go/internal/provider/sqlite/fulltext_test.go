package sqlite

import (
	"database/sql/driver"
	"testing"
)

func TestSQLiteFullTextFoldPreservesASCIIAndFoldsUnicode(t *testing.T) {
	for _, test := range []struct {
		name  string
		value driver.Value
		want  driver.Value
	}{{name: "null", value: nil, want: nil}, {name: "ascii", value: "alpha beta", want: "alpha beta"}, {name: "unicode", value: "Renée Καφές Tiếng Việt", want: "Renee Καφες Tieng Viet"}} {
		t.Run(test.name, func(t *testing.T) {
			got, err := sqliteFullTextFold([]driver.Value{test.value})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("folded value=%#v, want %#v", got, test.want)
			}
		})
	}
	if _, err := sqliteFullTextFold(nil); err == nil {
		t.Fatal("missing argument accepted")
	}
	if _, err := sqliteFullTextFold([]driver.Value{int64(1)}); err == nil {
		t.Fatal("non-text argument accepted")
	}
}
