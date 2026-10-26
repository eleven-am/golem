package normalize

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestConditionSealsOutputAndReturnsSealedInputUnchanged(t *testing.T) {
	model := modelID(1)
	a := scalar(t, model, fieldID(2), 2, nil)
	b := scalar(t, model, fieldID(1), 1, nil)
	input := logical(t, model, ir.LogicalAnd, constant(t, model, true), logical(t, model, ir.LogicalAnd, b, a), a)
	if ir.IsSealedNormal(input) {
		t.Fatal("unnormalized input reported as sealed")
	}
	got := normalize(t, input)
	if !ir.IsSealedNormal(got) {
		t.Fatal("normal form was not sealed")
	}
	if ir.IsSealedNormal(input) {
		t.Fatal("normalization sealed its unnormalized input")
	}
	again := normalize(t, got)
	if !bytes.Equal(canonical(t, again), canonical(t, got)) {
		t.Fatal("sealed normal form changed when normalized again")
	}
	_, children, _ := got.Logical()
	rebuilt := logical(t, model, ir.LogicalAnd, children...)
	if !bytes.Equal(canonical(t, normalize(t, rebuilt)), canonical(t, got)) {
		t.Fatal("unsealed copy of a normal form did not normalize to the same bytes")
	}
	wrapped := logical(t, model, ir.LogicalOr, got, b)
	unsealed := logical(t, model, ir.LogicalOr, logical(t, model, ir.LogicalAnd, children...), b)
	if !bytes.Equal(canonical(t, normalize(t, wrapped)), canonical(t, normalize(t, unsealed))) {
		t.Fatal("a sealed subtree normalized differently from its unsealed equivalent")
	}
}
