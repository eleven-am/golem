package events

import "testing"

func TestCDCCorrelationInputKeepsAnEmptyCursorEmpty(t *testing.T) {
	cursor := RuntimeCDCCorrelationInput("wal:1", []byte{}).Cursor()
	if cursor == nil || len(cursor) != 0 {
		t.Fatalf("cursor=%#v; want a non-nil empty cursor", cursor)
	}
}
