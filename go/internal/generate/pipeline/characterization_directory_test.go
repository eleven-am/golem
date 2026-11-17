package pipeline

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func validateCharacterizationSQLiteDirectory(parent string) error {
	if !filepath.IsAbs(parent) {
		return fmt.Errorf("characterization SQLite directory must be an existing absolute directory")
	}
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("characterization SQLite directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("characterization SQLite directory is not a directory")
	}
	return nil
}

// The override relocates only a newly owned database directory. Generated
// source, binaries and Go caches retain their ordinary temporary locations.
func characterizationSQLiteLocation(t testing.TB, parent string) (string, sqliteprovider.DurabilityProfile) {
	t.Helper()
	if parent == "" {
		return "file:" + filepath.Join(t.TempDir(), "home.db"), sqliteprovider.DurabilityWAL
	}
	if err := validateCharacterizationSQLiteDirectory(parent); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(parent, "golem-characterization-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove owned characterization directory: %v", err)
		}
	})
	t.Logf("owned SQLite characterization directory: %s", directory)
	location := (&url.URL{Scheme: "file", Path: filepath.Join(directory, "home.db")}).String()
	return location, sqliteprovider.DurabilityNetworkFilesystem
}

func TestCharacterizationSQLiteDirectoryOwnsOnlyScratchChild(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "scratch # parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(parent, "existing-file")
	if err := os.WriteFile(canary, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	var owned string
	t.Run("database", func(t *testing.T) {
		dsn, durability := characterizationSQLiteLocation(t, parent)
		location, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		owned = filepath.Dir(location.Path)
		if filepath.Dir(owned) != parent {
			t.Fatalf("database escaped its scratch parent: %s", owned)
		}
		database, _, err := sqliteprovider.New().OpenWithDurability(context.Background(), dsn, durability)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		var journal string
		if err := database.Get(&journal, "PRAGMA journal_mode"); err != nil || journal != "delete" || database.Stats().MaxOpenConnections != 1 {
			t.Fatalf("scratch database profile: journal=%s pool=%d err=%v", journal, database.Stats().MaxOpenConnections, err)
		}
		if _, err := database.Exec("CREATE TABLE fixture (id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned database directory survived cleanup: %v", err)
	}
	if contents, err := os.ReadFile(canary); err != nil || string(contents) != "keep" {
		t.Fatalf("scratch cleanup changed existing data: %q / %v", contents, err)
	}
}

func TestCharacterizationSQLiteDirectoryRejectsInvalidParents(t *testing.T) {
	parent := t.TempDir()
	file := filepath.Join(parent, "existing-file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative-directory", filepath.Join(parent, "missing-directory"), file} {
		if err := validateCharacterizationSQLiteDirectory(path); err == nil {
			t.Errorf("accepted invalid scratch parent %q", path)
		}
	}
}
