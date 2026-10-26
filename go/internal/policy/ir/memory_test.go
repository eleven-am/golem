package ir_test

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

const nestedDepth = 2048

func buildNested(tb testing.TB, leaves []ir.Condition) ir.Condition {
	current := leaves[0]
	for index := 1; index < len(leaves); index++ {
		operator := ir.LogicalAnd
		if index%2 == 0 {
			operator = ir.LogicalOr
		}
		next, err := ir.NewLogical(costModel(), operator, []ir.Condition{current, leaves[index]})
		if err != nil {
			tb.Fatal(err)
		}
		current = next
	}
	return current
}

func liveHeap() uint64 {
	var stats runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

func TestCanonicalEncodingRetainsLinearMemory(t *testing.T) {
	baseline := liveHeap()
	tree := buildNested(t, costLeaves(t, nestedDepth+1))
	built := liveHeap()
	encoded, err := ir.CanonicalCondition(tree)
	if err != nil {
		t.Fatal(err)
	}
	size := uint64(len(encoded))
	encoded = nil
	normalized, err := normalize.Condition(tree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ir.CanonicalCondition(normalized); err != nil {
		t.Fatal(err)
	}
	normalized = ir.Condition{}
	after := liveHeap()
	runtime.KeepAlive(tree)
	runtime.KeepAlive(encoded)
	runtime.KeepAlive(normalized)
	var growth uint64
	if after > built {
		growth = after - built
	}
	if growth > 4*size {
		t.Fatalf("encoding and normalizing retained %d bytes on a tree whose encoding is %d bytes", growth, size)
	}
	if built > baseline && built-baseline > 16*size {
		t.Fatalf("tree of %d nested nodes retains %d bytes for an encoding of %d bytes", nestedDepth, built-baseline, size)
	}
}

func sign(value int) int {
	switch {
	case value < 0:
		return -1
	case value > 0:
		return 1
	}
	return 0
}

func TestCanonicalKeyOrderMatchesEncodedBytes(t *testing.T) {
	leaves := costLeaves(t, 80)
	var relationField ir.FieldID
	relationField[15] = 9
	var relationID ir.RelationID
	relationID[15] = 1
	candidates := append([]ir.Condition(nil), leaves[:6]...)
	for _, size := range []int{2, 3, 40, 41, 80} {
		candidates = append(candidates, buildNested(t, leaves[:size]), buildBalanced(t, leaves[:size]), buildChain(t, leaves[:size]))
	}
	for _, child := range []*ir.Condition{nil, &leaves[0], &candidates[len(candidates)-1]} {
		relation, err := ir.NewRelation(costModel(), relationField, relationID, costModel(), ir.RelationToOne, ir.OperatorRelationIs, child, nil)
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, relation)
	}
	for _, left := range candidates {
		for _, right := range candidates {
			leftKey, err := ir.CanonicalKeyOf(left)
			if err != nil {
				t.Fatal(err)
			}
			rightKey, err := ir.CanonicalKeyOf(right)
			if err != nil {
				t.Fatal(err)
			}
			leftBytes, _ := ir.CanonicalCondition(left)
			rightBytes, _ := ir.CanonicalCondition(right)
			if got, want := sign(leftKey.Compare(rightKey)), bytes.Compare(leftBytes, rightBytes); got != want {
				t.Fatalf("key order %d disagrees with byte order %d", got, want)
			}
		}
	}
}
