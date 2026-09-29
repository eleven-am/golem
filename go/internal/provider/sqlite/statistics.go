package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type sqliteStatisticsExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

var sqliteStatisticsTables = []string{"sqlite_stat1", "sqlite_stat4"}

// ForgetShadowTableStatistics deletes the planner statistics SQLite recorded
// for virtual-table shadow tables and reports how many rows it removed.
func ForgetShadowTableStatistics(ctx context.Context, database sqliteStatisticsExecutor) (int64, error) {
	var forgotten int64
	for _, table := range sqliteStatisticsTables {
		present, err := sqliteTableExists(ctx, database, table)
		if err != nil {
			return forgotten, err
		}
		if !present {
			continue
		}
		stale, err := sqliteCount(ctx, database, `SELECT count(*) FROM `+table+` WHERE tbl IN (SELECT name FROM pragma_table_list WHERE schema='main' AND type='shadow')`)
		if err != nil {
			return forgotten, err
		}
		if stale == 0 {
			continue
		}
		result, err := database.ExecContext(ctx, `DELETE FROM `+table+` WHERE tbl IN (SELECT name FROM pragma_table_list WHERE schema='main' AND type='shadow')`)
		if err != nil {
			return forgotten, fmt.Errorf("sqlite shadow statistics: %w", err)
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return forgotten, fmt.Errorf("sqlite shadow statistics: %w", err)
		}
		forgotten += removed
	}
	return forgotten, nil
}

func sqliteTableExists(ctx context.Context, database sqliteStatisticsExecutor, name string) (bool, error) {
	present, err := sqliteCount(ctx, database, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name)
	return present != 0, err
}

func sqliteCount(ctx context.Context, database sqliteStatisticsExecutor, query string, arguments ...any) (int64, error) {
	rows, err := database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return 0, fmt.Errorf("sqlite shadow statistics: %w", err)
	}
	var count int64
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("sqlite shadow statistics: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("sqlite shadow statistics: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("sqlite shadow statistics: %w", err)
	}
	return count, nil
}

func sqliteQuotedIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
