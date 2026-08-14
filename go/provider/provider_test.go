package provider_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestUnsafeSQLXIsOnlyRawPoolEscape(t *testing.T) {
	typeOfDatabase := reflect.TypeOf((*provider.Database)(nil))
	if _, exists := typeOfDatabase.MethodByName("UnsafeSQLX"); !exists {
		t.Fatal("UnsafeSQLX is missing")
	}
	for _, forbidden := range []string{"SQLX", "DB", "Database", "Tx", "Begin"} {
		if _, exists := typeOfDatabase.MethodByName(forbidden); exists {
			t.Fatalf("raw pool has a safely named escape %s", forbidden)
		}
	}
}

func TestDatabaseValueCopiesShareCloseOwnership(t *testing.T) {
	database, err := providersqlite.Open(context.Background(), providersqlite.Config{
		DataSourceName: "file:p8_provider_copy_close?mode=memory&cache=shared",
	})
	if err != nil {
		t.Fatal(err)
	}
	copyOfDatabase := *database

	var wait sync.WaitGroup
	errors := make(chan error, 32)
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(useCopy bool) {
			defer wait.Done()
			if useCopy {
				errors <- copyOfDatabase.Close()
				return
			}
			errors <- database.Close()
		}(index%2 == 0)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	if database.UnsafeSQLX() != nil || copyOfDatabase.UnsafeSQLX() != nil {
		t.Fatal("a copied closed handle retained raw pool access")
	}
}

func TestDatabaseHandleCannotBeForgedOrProviderMismatched(t *testing.T) {
	var zero provider.Database
	if zero.Provider() != "" || zero.UnsafeSQLX() != nil || zero.Capabilities().Provider() != "" {
		t.Fatalf("zero handle published capabilities")
	}
	if err := zero.Close(); err != nil {
		t.Fatalf("zero close: %v", err)
	}

	database, err := providersqlite.Open(context.Background(), providersqlite.Config{
		DataSourceName: "file:p8_provider_handle?mode=memory&cache=shared",
	})
	if err != nil {
		t.Fatal(err)
	}
	if database.Provider() != golem.SQLite || database.Capabilities().Provider() != golem.SQLite {
		t.Fatalf("provider identity mismatch: handle=%q capabilities=%q", database.Provider(), database.Capabilities().Provider())
	}
	if database.UnsafeSQLX() == nil {
		t.Fatal("verified handle has no raw pool")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	if database.UnsafeSQLX() != nil {
		t.Fatal("closed handle retained raw pool access")
	}
}

func TestCapabilitiesReturnOwnedCanonicalFeatures(t *testing.T) {
	database, err := providersqlite.Open(context.Background(), providersqlite.Config{
		DataSourceName: "file:p8_provider_capabilities?mode=memory&cache=shared",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	first := database.Capabilities().Features()
	if len(first) == 0 {
		t.Fatal("verified features are empty")
	}
	first[0] = "forged"
	second := database.Capabilities().Features()
	if second[0] == "forged" {
		t.Fatal("capability feature slice aliases internal state")
	}
	for index := 1; index < len(second); index++ {
		if second[index-1] >= second[index] {
			t.Fatalf("features are not strictly canonical: %#v", second)
		}
	}
}
