package gentest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoldenVerifyIsReadOnlyAndUpdateIsExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "example.golden")

	if _, err := CompareGolden(path, []byte("first\n"), GoldenVerify); !errors.Is(err, ErrGoldenMissing) {
		t.Fatalf("verify missing golden error = %v, want ErrGoldenMissing", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verify mode created the golden: stat error = %v", err)
	}

	updated, err := CompareGolden(path, []byte("first\n"), GoldenUpdate)
	if err != nil {
		t.Fatalf("explicit update: %v", err)
	}
	if !updated.Updated {
		t.Fatal("explicit update did not report a rewrite")
	}

	verified, err := CompareGolden(path, []byte("first\n"), GoldenVerify)
	if err != nil {
		t.Fatalf("verify matching golden: %v", err)
	}
	if verified.Updated {
		t.Fatal("verify mode reported an update")
	}

	if _, err := CompareGolden(path, []byte("second\n"), GoldenVerify); err == nil {
		t.Fatal("verify accepted mismatched content")
	} else if !strings.Contains(err.Error(), "golden mismatch at byte 0") {
		t.Fatalf("mismatch error = %q", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first\n" {
		t.Fatalf("verify mutated content to %q", content)
	}
}

func TestGoldenRejectsUnknownMode(t *testing.T) {
	_, err := CompareGolden(filepath.Join(t.TempDir(), "x"), nil, GoldenMode(99))
	if err == nil || !strings.Contains(err.Error(), "unknown golden mode") {
		t.Fatalf("error = %v", err)
	}
}
