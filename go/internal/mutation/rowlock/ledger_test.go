package rowlock

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func testKey(t *testing.T, model byte, mode Mode, values ...policyir.Value) Key {
	t.Helper()
	components := make([]mutationdecode.IdentityComponent, len(values))
	for index, value := range values {
		component, err := mutationdecode.IdentityValue(policyir.FieldID{15: byte(index + 1)}, value)
		if err != nil {
			t.Fatal(err)
		}
		components[index] = component
	}
	identity, err := mutationdecode.NewIdentity(golem.KeyID{15: 1}, components)
	if err != nil {
		t.Fatal(err)
	}
	key, err := identityKey(policyir.ModelID{15: model}, identity, mode)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func mustValue(value policyir.Value, err error) policyir.Value {
	if err != nil {
		panic(err)
	}
	return value
}

func orders(keys []Key) []string {
	result := make([]string, len(keys))
	for index, key := range keys {
		result[index] = key.order
	}
	return result
}

func TestKeyOrderIsOneTotalOrderAcrossValueKindsModelsAndGuards(t *testing.T) {
	decimal := testKey(t, 2, Update, mustValue(policyir.NewDecimalValue(10, 1)))
	sameDecimal := testKey(t, 2, KeyShare, mustValue(policyir.NewDecimalValue(100, 2)))
	if decimal.order != sameDecimal.order {
		t.Fatal("equal decimals with different scales produced different lock keys")
	}
	keys := []Key{
		decimal,
		testKey(t, 2, Update, mustValue(policyir.NewDecimalValue(-25, 1))),
		testKey(t, 3, Update, policyir.BytesValue([]byte{0x00, 0xff})),
		testKey(t, 3, Update, policyir.BytesValue([]byte{0x00})),
		testKey(t, 4, Update, mustValue(policyir.NewDateTimeValue(1_700_000_000, 5000))),
		testKey(t, 4, Update, mustValue(policyir.NewDateTimeValue(1_700_000_000, 4000))),
		testKey(t, 5, Update, policyir.UUIDValue([16]byte{15: 1}), mustValue(policyir.StringValue("b"))),
		testKey(t, 5, Update, policyir.UUIDValue([16]byte{15: 1}), mustValue(policyir.StringValue("a"))),
		testKey(t, 1, Update, policyir.UUIDValue([16]byte{15: 9})),
		sameDecimal,
	}
	want := orders(strongestSorted(keys))
	if len(want) != len(keys)-1 {
		t.Fatalf("sorted keys=%d want %d after merging the equal decimal", len(want), len(keys)-1)
	}
	for index := 1; index < len(want); index++ {
		if want[index-1] >= want[index] {
			t.Fatalf("lock order is not strictly increasing at %d", index)
		}
	}
	for shift := range keys {
		rotated := append(append([]Key(nil), keys[shift:]...), keys[:shift]...)
		reversed := make([]Key, len(rotated))
		for index := range rotated {
			reversed[len(rotated)-1-index] = rotated[index]
		}
		for _, permutation := range [][]Key{rotated, reversed} {
			got := orders(strongestSorted(permutation))
			for index := range want {
				if got[index] != want[index] {
					t.Fatalf("lock order depends on request order at %d", index)
				}
			}
		}
	}
	models := strongestSorted(keys)
	for index := 1; index < len(models); index++ {
		previous, current := models[index-1].model, models[index].model
		if string(previous[:]) > string(current[:]) {
			t.Fatal("a model's rows sort after a later model's rows")
		}
	}
	if guardOrder([32]byte{0xff}) >= want[0] {
		t.Fatal("a selector guard does not sort before every row")
	}
	for _, key := range strongestSorted(keys) {
		if key.order == decimal.order && key.mode != Update {
			t.Fatal("a key requested twice did not keep the stronger mode")
		}
	}
}

func TestPlanTakesUpgradesAndOutOfOrderRowsWithoutWaiting(t *testing.T) {
	first := testKey(t, 1, Update, policyir.UUIDValue([16]byte{15: 1}))
	held := testKey(t, 1, KeyShare, policyir.UUIDValue([16]byte{15: 2}))
	last := testKey(t, 1, KeyShare, policyir.UUIDValue([16]byte{15: 3}))
	ledger := NewLedger()
	ledger.record([]Key{held})
	upgrade := held
	upgrade.mode = Update
	pending := plan(ledger, []Key{last, upgrade, first, held})
	if len(pending) != 3 {
		t.Fatalf("pending=%d want 3", len(pending))
	}
	want := []struct {
		order  string
		mode   Mode
		nowait bool
	}{{first.order, Update, true}, {held.order, Update, true}, {last.order, KeyShare, false}}
	for index, expected := range want {
		got := pending[index]
		if got.key.order != expected.order || got.key.mode != expected.mode || got.nowait != expected.nowait {
			t.Fatalf("pending[%d]=mode %d nowait %t; want mode %d nowait %t", index, got.key.mode, got.nowait, expected.mode, expected.nowait)
		}
	}
	lone := NewLedger()
	lone.record([]Key{held})
	only := plan(lone, []Key{upgrade})
	if len(only) != 1 || !only[0].nowait {
		t.Fatal("an upgrade of the highest held key waits instead of taking NOWAIT")
	}
	if again := plan(ledger, []Key{held}); len(again) != 0 {
		t.Fatal("a key already held in the requested mode was locked again")
	}
}

func TestRollbackRestoresADowngradedMode(t *testing.T) {
	key := testKey(t, 1, KeyShare, policyir.UUIDValue([16]byte{15: 7}))
	ledger := NewLedger()
	ledger.record([]Key{key})
	mark := ledger.Mark()
	upgraded := key
	upgraded.mode = Update
	ledger.record([]Key{upgraded})
	held, _ := ledger.snapshot()
	if held[key.order] != Update {
		t.Fatal("the upgrade was not recorded")
	}
	ledger.RollbackTo(mark)
	held, _ = ledger.snapshot()
	if held[key.order] != KeyShare {
		t.Fatalf("rollback left mode %d; want the KEY SHARE held before the savepoint", held[key.order])
	}
	ledger.RollbackTo(0)
	held, _ = ledger.snapshot()
	if _, present := held[key.order]; present {
		t.Fatal("rollback to the start left the key held")
	}
}
