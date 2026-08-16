package pipeline

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	postgresqlprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// TestQueueStorageIsAllowlistedByEveryLowering proves the durable job queue's
// provider-owned storage reaches every provider's reviewed schema without a
// generation option. The allowlist tolerates these objects rather than
// requiring them, so an application that never starts a worker carries the
// names and no storage.
func TestQueueStorageIsAllowlistedByEveryLowering(t *testing.T) {
	request := multipackageRequest(t)
	request.Lowerers = []physical.Lowerer{sqliteprovider.New(), postgresqlprovider.New()}
	built, err := Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	expected := physical.QueueUnmanagedObjects()
	sort.Slice(expected, func(left, right int) bool {
		if expected[left].Kind != expected[right].Kind {
			return expected[left].Kind < expected[right].Kind
		}
		return expected[left].Name < expected[right].Name
	})
	if len(built.Providers) == 0 {
		t.Fatal("no providers were lowered")
	}
	for _, provider := range built.Providers {
		if !reflect.DeepEqual(provider.Schema.Unmanaged, expected) {
			t.Fatalf("provider %s allowlisted %#v; want %#v",
				provider.Provider.Provider, provider.Schema.Unmanaged, expected)
		}
		if provider.Schema.Tables == nil {
			t.Fatalf("provider %s lowered no managed tables", provider.Provider.Provider)
		}
	}
}
