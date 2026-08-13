package postgresql_test

import (
	"context"

	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/jmoiron/sqlx"
)

func compilePostgreSQLQueryPlanCaptureBoundary() {
	var capture func(context.Context, *sqlx.Conn, string, []any, *schema.Registry, queryplancapture.AliasMap) (queryplancapture.Plan, error)
	capture = postgresprovider.CaptureQueryPlan
	_ = capture
}
