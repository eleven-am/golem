package runtime

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/provider/restoreprobe"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

const restoreCrashEnvironment = "GOLEM_RESTORE_CRASH"

func TestSQLiteRestoreCrashChild(t *testing.T) {
	if os.Getenv(restoreCrashEnvironment) != "1" {
		t.Skip("parent-owned crash child only")
	}
	// The existing fixture supplies exactly the same generated bindings and
	// reviewed bundle as the parent. TMPDIR is inside the parent's disposable root.
	fixture := sqliteRestoreFixture(t)
	fixture.stop()
	if err := fixture.applicationConfig.Database.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	boundary := os.Getenv("GOLEM_RESTORE_BOUNDARY")
	ctx = restoreprobe.WithHook(ctx, func(actual string) {
		if actual != boundary {
			return
		}
		ready := os.NewFile(3, "restore-boundary")
		if ready == nil {
			t.Fatal("missing parent readiness pipe")
		}
		if _, err := fmt.Fprintln(ready, actual); err != nil {
			t.Fatal(err)
		}
		if err := ready.Close(); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		t.Fatal("parent did not kill restore child")
	})
	session, err := publicsqlite.PrepareRestore(ctx, publicsqlite.RestoreConfig{Archive: os.Getenv("GOLEM_RESTORE_ARCHIVE"), Destination: os.Getenv("GOLEM_RESTORE_DESTINATION"), ArchiveBundle: fixture.schema.Bundle, TargetBundle: fixture.schema.Bundle, Mode: publicsqlite.RestoreResumeAtLeastOnce})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Database().Close(); err != nil {
		t.Fatal(err)
	}
	_, err = session.Publish(ctx)
	t.Fatalf("child escaped crash boundary: %v", err)
}

// Killing a process after the link/rename tests visible atomic publication. It
// does not simulate a power failure or prove durability before directory sync.
func TestSQLiteRestoreProcessInterruptionAtPublication(t *testing.T) {
	for _, boundary := range []string{restoreprobe.BeforePublication, restoreprobe.AfterPublication} {
		for _, replace := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replace=%t", boundary, replace), func(t *testing.T) {
				fixture := sqliteRestoreFixture(t)
				fixture.mutate(t, golem.EventCreated, 91, 1, "archived")
				expected := sqliteSnapshotRuntimeState(t, fixture.app.database)
				archivedIdentity := fixture.app.DataIncarnation()
				root := t.TempDir()
				archive := filepath.Join(root, "archive.db")
				original := sqliteRestoreArchive(t, fixture, archive)
				destination := filepath.Join(root, "destination.db")
				var previous []byte
				if replace {
					fixture.mutate(t, golem.EventUpdated, 91, 1, "old destination")
					previous = sqliteRestoreArchive(t, fixture, destination)
				}
				fixture.stop()
				if err := fixture.applicationConfig.Database.Close(); err != nil {
					t.Fatal(err)
				}
				killSQLiteRestoreChild(t, root, archive, destination, boundary)
				if boundary == restoreprobe.BeforePublication {
					got, err := os.ReadFile(destination)
					if replace {
						if err != nil || !bytes.Equal(got, previous) {
							t.Fatal("prepublication crash altered previous destination", err)
						}
					} else if !os.IsNotExist(err) {
						t.Fatal("prepublication crash exposed destination", err)
					}
					// An abandoned private staging directory is never reused or auto-admitted.
					retry, err := publicsqlite.PrepareRestore(context.Background(), publicsqlite.RestoreConfig{Archive: archive, Destination: destination, ArchiveBundle: fixture.schema.Bundle, TargetBundle: fixture.schema.Bundle, Mode: publicsqlite.RestoreResumeAtLeastOnce})
					if err != nil {
						t.Fatal(err)
					}
					defer retry.Close()
					if err := retry.Database().Close(); err != nil {
						t.Fatal(err)
					}
					if _, err := retry.Publish(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				// After known publication, open the resulting image without restoring again.
				database, err := publicsqlite.Open(context.Background(), publicsqlite.Config{DataSourceName: destination})
				if err != nil {
					t.Fatal(err)
				}
				defer database.Close()
				config := fixture.applicationConfig
				config.Database = database
				app, err := Open(context.Background(), config)
				if err != nil {
					t.Fatal("published image failed actual runtime/catalog/ledger admission", err)
				}
				if !app.DataIncarnation().Valid() || app.DataIncarnation() == archivedIdentity {
					t.Fatal("publication did not retain a fresh identity")
				}
				if actual := sqliteSnapshotRuntimeState(t, database.UnsafeSQLX()); !reflect.DeepEqual(expected, actual) {
					t.Fatal("publication changed archived facts, versions or counters")
				}
				var title string
				if err := database.UnsafeSQLX().Get(&title, "SELECT title FROM posts WHERE id=?", (golem.UUID{15: 91}).String()); err != nil || title != "archived" {
					t.Fatalf("published row=%q error=%v", title, err)
				}
				if got, err := os.ReadFile(archive); err != nil || !bytes.Equal(got, original) {
					t.Fatal("crash/retry changed immutable archive", err)
				}
			})
		}
	}
}

func killSQLiteRestoreChild(t *testing.T, root, archive, destination, boundary string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	command := exec.Command(executable, "-test.run=^TestSQLiteRestoreCrashChild$", "-test.count=1", "-test.timeout=50s")
	command.Env = append(os.Environ(), restoreCrashEnvironment+"=1", "TMPDIR="+root, "GOLEM_RESTORE_ARCHIVE="+archive, "GOLEM_RESTORE_DESTINATION="+destination, "GOLEM_RESTORE_BOUNDARY="+boundary)
	command.ExtraFiles = []*os.File{writer}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	writer.Close()
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(reader).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != boundary+"\n" {
			t.Fatalf("child failed before expected boundary: %q", line)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("restore child readiness timed out")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("crash child exited successfully")
	}
	waited = true
}
