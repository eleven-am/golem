package runtime

import (
	"testing"

	readsql "github.com/eleven-am/golem/go/internal/read/sql"
)

func TestReadLimitsNormalizeWithoutInventingRowCaps(t *testing.T) {
	limits, err := normalizeReadLimits(ReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if limits.plan.MaxTake != 0 || limits.plan.MaxRelationFanout != 0 {
		t.Fatalf("invented row caps: root=%d fanout=%d", limits.plan.MaxTake, limits.plan.MaxRelationFanout)
	}
	if limits.plan.MaxRelationDepth != 5 || limits.plan.MaxSelected != 256 || limits.plan.MaxStatementParameters != 999 || limits.plan.MaxStatementBytes != readsql.MaxStatementBytes || limits.plan.MaxStatementAliases != readsql.MaxStatementAliases || limits.loaderKeys != MaxBatchLoaderKeys {
		t.Fatalf("normalized defaults=%+v loader=%d", limits.plan, limits.loaderKeys)
	}
}

func TestReadLimitsAcceptLowerBoundsAndRejectRaisedHardCeilings(t *testing.T) {
	want := ReadLimits{MaxTake: 7, MaxRelationFanout: 3, MaxRelationDepth: 2, MaxSelectedFields: 9, MaxStatementParameters: 100, MaxStatementBytes: 2_000, MaxStatementAliases: 20, MaxLoaderKeys: 30}
	got, err := normalizeReadLimits(want)
	if err != nil {
		t.Fatal(err)
	}
	if got.plan.MaxTake != 7 || got.plan.MaxRelationFanout != 3 || got.plan.MaxRelationDepth != 2 || got.plan.MaxSelected != 9 || got.plan.MaxStatementParameters != 100 || got.plan.MaxStatementBytes != 2_000 || got.plan.MaxStatementAliases != 20 || got.loaderKeys != 30 {
		t.Fatalf("normalized limits=%+v loader=%d", got.plan, got.loaderKeys)
	}
	invalid := []ReadLimits{
		{MaxTake: -1},
		{MaxRelationFanout: -1},
		{MaxStatementParameters: readsql.MaxStatementParameters + 1},
		{MaxStatementBytes: readsql.MaxStatementBytes + 1},
		{MaxStatementAliases: readsql.MaxStatementAliases + 1},
		{MaxLoaderKeys: MaxBatchLoaderKeys + 1},
	}
	for index, value := range invalid {
		if _, err := normalizeReadLimits(value); err == nil {
			t.Fatalf("invalid case %d was accepted: %+v", index, value)
		}
	}
}
