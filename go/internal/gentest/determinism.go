package gentest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

const defaultDeterminismSeed int64 = 0x474f4c454d

// DeterminismOptions controls reproducible randomized traversal checks.
type DeterminismOptions struct {
	Seed       int64
	Iterations int
}

// CheckDeterminism calls render with reproducibly different random sources and
// requires byte-identical output. render should use the supplied source to
// perturb all traversal orders that are semantically unordered.
func CheckDeterminism(
	options DeterminismOptions,
	render func(random *rand.Rand) ([]byte, error),
) ([]byte, error) {
	if render == nil {
		return nil, errors.New("determinism render function is nil")
	}
	if options.Iterations < 2 {
		return nil, fmt.Errorf("determinism iterations must be at least 2, got %d", options.Iterations)
	}
	seed := options.Seed
	if seed == 0 {
		seed = defaultDeterminismSeed
	}

	var baseline []byte
	var baselineSeed int64
	for iteration := 0; iteration < options.Iterations; iteration++ {
		iterationSeed := derivedSeed(seed, iteration)
		output, err := render(rand.New(rand.NewSource(iterationSeed)))
		if err != nil {
			return nil, fmt.Errorf("determinism iteration %d seed %d: %w", iteration, iterationSeed, err)
		}
		if iteration == 0 {
			baseline = append([]byte(nil), output...)
			baselineSeed = iterationSeed
			continue
		}
		if !bytes.Equal(baseline, output) {
			return nil, fmt.Errorf(
				"nondeterministic output at iteration %d seed %d: baseline seed %d sha256=%x, got sha256=%x",
				iteration,
				iterationSeed,
				baselineSeed,
				sha256.Sum256(baseline),
				sha256.Sum256(output),
			)
		}
	}
	return baseline, nil
}

// RequireDeterminism is the testing.TB adapter for CheckDeterminism.
func RequireDeterminism(
	t testing.TB,
	options DeterminismOptions,
	render func(random *rand.Rand) ([]byte, error),
) []byte {
	t.Helper()
	output, err := CheckDeterminism(options, render)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

// ShuffleCopy returns a shuffled copy and never mutates the caller's slice.
func ShuffleCopy[T any](random *rand.Rand, values []T) []T {
	shuffled := append([]T(nil), values...)
	random.Shuffle(len(shuffled), func(left, right int) {
		shuffled[left], shuffled[right] = shuffled[right], shuffled[left]
	})
	return shuffled
}

func derivedSeed(seed int64, iteration int) int64 {
	// The odd Weyl increment gives every practical iteration a distinct stream
	// without depending on wall time or global math/rand state.
	return seed + int64(iteration)*-7046029254386353131
}
