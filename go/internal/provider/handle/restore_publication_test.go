package handle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/dataincarnation"
)

// These are filesystem publication-component tests. Public restore verification
// is deliberately outside this boundary; real Link/Rename and directory Sync
// failures exercise the irreversible publication result without injected hooks.
func TestSQLiteRestorePublicationReportsDirectoryFailureAfterLinkOrReplace(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "new-link"
		if replace {
			name = "existing-replace"
		}
		t.Run(name, func(t *testing.T) {
			restore, parent := restorePublicationFixture(t)
			if replace {
				if err := os.WriteFile(restore.destination, []byte("previous-image"), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				restore.previous, err = os.Stat(restore.destination)
				if err != nil {
					t.Fatal(err)
				}
			}
			identity, err := dataincarnation.New()
			if err != nil {
				t.Fatal(err)
			}
			directory, err := os.Open(parent)
			if err != nil {
				t.Fatal(err)
			}
			// Publication uses owned paths and succeeds, while syncing this real
			// closed directory FD must fail after the destination becomes visible.
			if err := directory.Close(); err != nil {
				t.Fatal(err)
			}
			returned, publishErr := restore.publishVerifiedImage(context.Background(), directory, identity)
			if code, ok := CodeOf(publishErr); !ok || code != CodeRestorePublished {
				t.Fatalf("post-publication error=%v, want %s", publishErr, CodeRestorePublished)
			}
			if !returned.Valid() || returned != identity || restore.identity != identity || !restore.published {
				t.Fatal("publication lost its committed identity or published state")
			}
			assertRestorePublicationBytes(t, restore.destination, "verified-new-image")
			if _, err := os.Stat(restore.directory); err != nil {
				t.Fatalf("failure unexpectedly lost staging ownership: %v", err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				closeErr := restore.Close()
				if code, ok := CodeOf(closeErr); !ok || code != CodeRestorePublished || closeErr != publishErr {
					t.Fatalf("close %d lost stable published error: %v", attempt, closeErr)
				}
				assertRestorePublicationBytes(t, restore.destination, "verified-new-image")
				if !restore.published || restore.identity != identity {
					t.Fatal("close erased publication state")
				}
			}
			if _, err := os.Stat(restore.directory); !os.IsNotExist(err) {
				t.Fatalf("close retained owned staging: %v", err)
			}
			if _, err := os.Stat(parent); err != nil {
				t.Fatalf("close removed destination parent: %v", err)
			}
		})
	}
}

func TestSQLiteRestorePublicationRefusesNewDestinationRace(t *testing.T) {
	restore, parent := restorePublicationFixture(t)
	// previous stays nil: another owner creates the destination after the
	// coordinator's absence check but before its no-clobber link syscall.
	if err := os.WriteFile(restore.destination, []byte("concurrent-owner"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := dataincarnation.New()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	returned, publishErr := restore.publishVerifiedImage(context.Background(), directory, identity)
	if code, ok := CodeOf(publishErr); !ok || code != CodeMaintenance {
		t.Fatalf("destination race error=%v, want %s", publishErr, CodeMaintenance)
	}
	if returned.Valid() || restore.identity.Valid() || restore.published {
		t.Fatal("failed link reported publication")
	}
	assertRestorePublicationBytes(t, restore.destination, "concurrent-owner")
	assertRestorePublicationBytes(t, restore.stage, "verified-new-image")
	for attempt := 0; attempt < 2; attempt++ {
		if err := restore.Close(); err != nil {
			t.Fatalf("unpublished close: %v", err)
		}
		assertRestorePublicationBytes(t, restore.destination, "concurrent-owner")
	}
	if _, err := os.Stat(restore.directory); !os.IsNotExist(err) {
		t.Fatalf("unpublished close retained staging: %v", err)
	}
}

func restorePublicationFixture(t *testing.T) (*SQLiteRestore, string) {
	t.Helper()
	parent := t.TempDir()
	directory, err := os.MkdirTemp(parent, ".golem-restore-test-")
	if err != nil {
		t.Fatal(err)
	}
	restore := &SQLiteRestore{directory: directory, stage: filepath.Join(directory, "restore.db"), destination: filepath.Join(parent, "live.db")}
	if err := os.WriteFile(restore.stage, []byte("verified-new-image"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restore.Close() })
	return restore, parent
}

func assertRestorePublicationBytes(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("publication bytes=%q, want %q: %v", got, want, err)
	}
}
