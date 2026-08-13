package sqlite_test

import (
	"context"

	"github.com/eleven-am/golem/go/internal/policy/schema"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/jmoiron/sqlx"
)

func compileSQLiteQueryPlanCaptureBoundary() {
	var capture func(context.Context, *sqlx.Conn, string, []any, *schema.Registry, queryplancapture.AliasMap) (queryplancapture.Plan, error)
	capture = sqliteprovider.CaptureQueryPlan
	_ = capture
}
