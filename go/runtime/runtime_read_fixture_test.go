package runtime

import (
	"fmt"
	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type oracleUser struct{}
type oraclePost struct{}

type oraclePrincipal struct {
	UserPrefix string
	PostPrefix string
}

type oracleActor = oraclePrincipal

func nestedAcceptanceTable[P, A any](app *App[P, A], model golem.ModelID) string {
	provider := golem.SQLite
	if app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	physicalModel, _ := app.registry.PhysicalModel(provider, model)
	if provider == golem.SQLite {
		return `"` + string(physicalModel.Name()) + `"`
	}
	namespace, _ := app.registry.PhysicalNamespace(provider)
	return `"` + string(namespace) + `"."` + string(physicalModel.Name()) + `"`
}

func mutationResultUUIDText(last byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012x", last)
}
