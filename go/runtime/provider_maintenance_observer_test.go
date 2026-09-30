package runtime

import (
	"testing"

	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
)

func TestOpenRoutesProviderMaintenanceFailuresToTheConfiguredObserver(t *testing.T) {
	recorder := &queryPlanObservationRecorder{}
	fixture := newQueryPlanFixture(t, recorder)
	if got := (*providerhandle.Database)(fixture.app.databaseHandle).MaintenanceObserverForTest(); got != recorder {
		t.Fatalf("provider maintenance observer=%#v, want the application's configured observer", got)
	}
}
