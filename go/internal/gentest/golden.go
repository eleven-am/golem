package gentest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// GoldenMode makes golden rewrites an explicit test decision. The package does
// not inspect environment variables or command-line flags on its own.
type GoldenMode uint8

const (
	GoldenVerify GoldenMode = iota
	GoldenUpdate
)

var ErrGoldenMissing = errors.New("golden file is missing")

// GoldenResult describes the comparison without coupling callers to testing.T.
type GoldenResult struct {
	Updated bool
}

// CompareGolden compares got byte-for-byte with path. GoldenVerify is read-only.
// GoldenUpdate atomically replaces the file with got when it differs.
func CompareGolden(path string, got []byte, mode GoldenMode) (GoldenResult, error) {
	if mode != GoldenVerify && mode != GoldenUpdate {
		return GoldenResult{}, fmt.Errorf("unknown golden mode %d", mode)
	}

	want, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return GoldenResult{}, fmt.Errorf("read golden %q: %w", path, err)
		}
		if mode == GoldenVerify {
			return GoldenResult{}, fmt.Errorf("%w: %s", ErrGoldenMissing, path)
		}
		if err := writeFileAtomically(path, got); err != nil {
			return GoldenResult{}, err
		}
		return GoldenResult{Updated: true}, nil
	}

	if bytes.Equal(want, got) {
		return GoldenResult{}, nil
	}
	if mode == GoldenUpdate {
		if err := writeFileAtomically(path, got); err != nil {
			return GoldenResult{}, err
		}
		return GoldenResult{Updated: true}, nil
	}

	offset := firstDifference(want, got)
	return GoldenResult{}, fmt.Errorf(
		"golden mismatch at byte %d for %s: want sha256=%x (%d bytes), got sha256=%x (%d bytes)",
		offset,
		path,
		sha256.Sum256(want),
		len(want),
		sha256.Sum256(got),
		len(got),
	)
}

// RequireGolden is the testing.TB adapter for CompareGolden.
func RequireGolden(t testing.TB, path string, got []byte, mode GoldenMode) GoldenResult {
	t.Helper()
	result, err := CompareGolden(path, got, mode)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func firstDifference(left, right []byte) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			return index
		}
	}
	return limit
}

func writeFileAtomically(path string, content []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create golden directory %q: %w", directory, err)
	}

	temporary, err := os.CreateTemp(directory, ".golem-golden-*")
	if err != nil {
		return fmt.Errorf("create temporary golden beside %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set temporary golden permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write temporary golden: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary golden: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary golden: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace golden %q: %w", path, err)
	}
	keep = true
	return nil
}
