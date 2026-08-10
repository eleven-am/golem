package gentest

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
)

func TestDeterminismRunnerProvesCanonicalOutputUnderShuffledInputs(t *testing.T) {
	input := []string{"posts", "users", "comments", "tags", "post_tags"}
	output, err := CheckDeterminism(
		DeterminismOptions{Seed: 17, Iterations: 50},
		func(random *rand.Rand) ([]byte, error) {
			traversal := ShuffleCopy(random, input)
			slices.Sort(traversal)
			return []byte(strings.Join(traversal, "\n") + "\n"), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "comments\npost_tags\nposts\ntags\nusers\n" {
		t.Fatalf("canonical output = %q", output)
	}
}

func TestDeterminismRunnerReportsTraversalDependentOutput(t *testing.T) {
	input := []string{"posts", "users", "comments", "tags", "post_tags"}
	_, err := CheckDeterminism(
		DeterminismOptions{Seed: 17, Iterations: 50},
		func(random *rand.Rand) ([]byte, error) {
			return []byte(strings.Join(ShuffleCopy(random, input), ",")), nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "nondeterministic output") {
		t.Fatalf("error = %v", err)
	}
}

func TestShuffleCopyDoesNotMutateInput(t *testing.T) {
	input := []int{1, 2, 3, 4, 5}
	original := append([]int(nil), input...)
	_ = ShuffleCopy(rand.New(rand.NewSource(1)), input)
	if !slices.Equal(input, original) {
		t.Fatalf("input mutated: got %v, want %v", input, original)
	}
}
