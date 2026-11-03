package rowversion

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

const sequenceName = physical.RowVersionObject

func Column() physical.PhysicalName { return physical.RowVersionColumn }

func Next(provider policyir.Provider, systemNamespace physical.PhysicalName) (string, error) {
	switch provider {
	case policyir.ProviderPostgreSQL:
		if systemNamespace == "" || strings.ContainsAny(string(systemNamespace), "\x00\"'") {
			return "", fmt.Errorf("row version sequence needs a valid system namespace")
		}
		return `nextval('"` + string(systemNamespace) + `"."` + string(sequenceName) + `"')`, nil
	case policyir.ProviderSQLite:
		return `((SELECT "value" FROM "` + string(sequenceName) + `") + 1)`, nil
	default:
		return "", fmt.Errorf("row version provider %d is unsupported", provider)
	}
}

func Advances(provider policyir.Provider) bool { return provider == policyir.ProviderSQLite }

func Advance(returning string) string {
	return `UPDATE "` + string(sequenceName) + `" SET "value" = "value" + 1 RETURNING ` + returning
}

func AdvanceColumn() physical.PhysicalName { return "value" }
