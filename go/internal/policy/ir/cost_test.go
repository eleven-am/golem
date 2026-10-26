package ir_test

import (
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

const (
	buildAllocsPerNode     = 4
	normalizeAllocsPerNode = 24
)

var costSizes = []int{64, 256, 1024}

func costModel() ir.ModelID {
	var id ir.ModelID
	id[15] = 1
	return id
}

func costLeaves(tb testing.TB, n int) []ir.Condition {
	tb.Helper()
	var field ir.FieldID
	field[15] = 1
	typ, err := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		tb.Fatal(err)
	}
	requirement, err := ir.NewRequirement(ir.PortableProviders(), ir.CapabilityBinaryText)
	if err != nil {
		tb.Fatal(err)
	}
	leaves := make([]ir.Condition, n)
	for index := range leaves {
		value, err := ir.SignedValue(ir.ValueInt64, int64(index))
		if err != nil {
			tb.Fatal(err)
		}
		operand, err := ir.OneOperand(value)
		if err != nil {
			tb.Fatal(err)
		}
		leaves[index], err = ir.NewScalar(costModel(), field, typ, ir.OperatorEqual, ir.ComparisonSensitive, operand, []ir.Requirement{requirement})
		if err != nil {
			tb.Fatal(err)
		}
	}
	return leaves
}

func buildChain(tb testing.TB, leaves []ir.Condition) ir.Condition {
	current := leaves[0]
	for index := 1; index < len(leaves); index++ {
		next, err := ir.NewLogical(costModel(), ir.LogicalAnd, []ir.Condition{current, leaves[index]})
		if err != nil {
			tb.Fatal(err)
		}
		current = next
	}
	return current
}

func buildBalanced(tb testing.TB, leaves []ir.Condition) ir.Condition {
	if len(leaves) == 1 {
		return leaves[0]
	}
	middle := len(leaves) / 2
	operator := ir.LogicalAnd
	if len(leaves)%4 == 0 {
		operator = ir.LogicalOr
	}
	node, err := ir.NewLogical(costModel(), operator, []ir.Condition{buildBalanced(tb, leaves[:middle]), buildBalanced(tb, leaves[middle:])})
	if err != nil {
		tb.Fatal(err)
	}
	return node
}

var costShapes = []struct {
	name  string
	build func(testing.TB, []ir.Condition) ir.Condition
}{
	{name: "chain", build: buildChain},
	{name: "balanced", build: buildBalanced},
}

func TestBuildCostIsLinear(t *testing.T) {
	for _, shape := range costShapes {
		for _, n := range costSizes {
			t.Run(fmt.Sprintf("%s/n=%d", shape.name, n), func(t *testing.T) {
				leaves := costLeaves(t, n)
				build := testing.AllocsPerRun(1, func() { shape.build(t, leaves) })
				if limit := float64(buildAllocsPerNode * n); build > limit {
					t.Fatalf("build allocations=%.0f exceed %.0f for n=%d", build, limit, n)
				}
				tree := shape.build(t, leaves)
				normalized := testing.AllocsPerRun(1, func() {
					if _, err := normalize.Condition(tree); err != nil {
						t.Fatal(err)
					}
				})
				if limit := float64(normalizeAllocsPerNode * n); normalized > limit {
					t.Fatalf("normalize allocations=%.0f exceed %.0f for n=%d", normalized, limit, n)
				}
			})
		}
	}
}

func BenchmarkNewLogical(b *testing.B) {
	for _, shape := range costShapes {
		for _, n := range costSizes {
			b.Run(fmt.Sprintf("%s/n=%d", shape.name, n), func(b *testing.B) {
				leaves := costLeaves(b, n)
				b.ReportAllocs()
				for b.Loop() {
					shape.build(b, leaves)
				}
			})
		}
	}
}

func BenchmarkNormalize(b *testing.B) {
	for _, shape := range costShapes {
		for _, n := range costSizes {
			b.Run(fmt.Sprintf("%s/n=%d", shape.name, n), func(b *testing.B) {
				tree := shape.build(b, costLeaves(b, n))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := normalize.Condition(tree); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
