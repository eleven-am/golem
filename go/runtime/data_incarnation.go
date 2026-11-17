package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

// DataIncarnation returns the immutable provider identity admitted at startup.
// It does not rotate storage or fence an already running application fleet.
func (app *App[P, A]) DataIncarnation() golem.DataIncarnation {
	if app == nil {
		return golem.DataIncarnation{}
	}
	return app.dataIncarnation
}

func loadDataIncarnation(ctx context.Context, database *sqlx.DB, provider golem.Provider, system physical.SystemSchema, bundle golem.SchemaBundle) (golem.DataIncarnation, error) {
	compilation, err := schema.DecodeActive(bundle)
	if err != nil {
		return golem.DataIncarnation{}, err
	}
	if compilation.Contract.DataIncarnation == nil {
		return golem.DataIncarnation{}, nil
	}
	var value golem.DataIncarnation
	switch provider {
	case golem.SQLite:
		value, err = sqlite.New().ReadDataIncarnation(ctx, database, system)
	case golem.PostgreSQL:
		value, err = postgresql.New().ReadDataIncarnation(ctx, database, system)
	default:
		err = fmt.Errorf("unsupported data identity provider")
	}
	if err != nil || !value.Valid() {
		if ctx.Err() != nil {
			return golem.DataIncarnation{}, ctx.Err()
		}
		return golem.DataIncarnation{}, fmt.Errorf("durable data identity is invalid")
	}
	return value, nil
}
