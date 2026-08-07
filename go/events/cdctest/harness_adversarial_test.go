package cdctest

import (
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

func TestP7CDCReplayComparisonIncludesExactImageContents(t *testing.T) {
	model := golem.ModelID{15: 1}
	field := golem.FieldID{15: 2}
	firstRow, err := golem.RuntimeCDCModelRow(model, golem.RuntimePresentReadCell(field, "before", nil))
	if err != nil {
		t.Fatal(err)
	}
	changedRow, err := golem.RuntimeCDCModelRow(model, golem.RuntimePresentReadCell(field, "after", nil))
	if err != nil {
		t.Fatal(err)
	}
	first := events.CDCBatchInput{
		SourceTransactionID: "source-1",
		RecordedAt:          time.Date(2026, 8, 7, 12, 0, 0, 123456000, time.UTC),
		Cursor:              []byte{1},
		Changes: []events.CDCChangeInput{{
			Ordinal: 1, Model: model, Action: golem.EventCreated, After: &firstRow,
		}},
	}
	changed := cloneBatch(first)
	changed.Changes[0].After = &changedRow
	if sameBatchShape(first, changed) {
		t.Fatal("CDC conformance comparison ignored a replayed exact-image value change")
	}

	ordinary, err := golem.RuntimeModelReadRow(model, golem.RuntimePresentReadCell(field, "before", nil))
	if err != nil {
		t.Fatal(err)
	}
	changed = cloneBatch(first)
	changed.Changes[0].After = &ordinary
	if sameBatchShape(first, changed) {
		t.Fatal("CDC conformance comparison accepted an ordinary projected row as an exact image")
	}

	changed = cloneBatch(first)
	changed.RecordedAt = changed.RecordedAt.Add(time.Microsecond)
	if sameBatchShape(first, changed) {
		t.Fatal("CDC conformance comparison ignored replay timestamp drift")
	}
}
