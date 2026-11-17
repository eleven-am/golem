package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
	internalpostgresql "github.com/eleven-am/golem/go/internal/provider/postgresql"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/provider"
	publicpostgresql "github.com/eleven-am/golem/go/provider/postgresql"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// Upgrade is an operator-owned offline transition, not a fleet lock. Every
// producer and worker, including old binaries, must already have stopped.
func runQueueStorage(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "storage" || (args[1] != "inspect" && args[1] != "upgrade") {
		writeError(stderr, errors.New(usage))
		return 2
	}
	operation := args[1]
	flags := flag.NewFlagSet("queue storage "+operation, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	kind := flags.String("provider", "", "database provider")
	dsn := flags.String("dsn", "", "explicit existing database data source name")
	namespace := flags.String("system-namespace", "", "existing provider system namespace")
	profile := flags.String("sqlite-profile", "local", "SQLite profile: local, network, or network-wal")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *dsn == "" || (*kind != "sqlite" && *kind != "postgresql") || (*profile != "local" && *profile != "network" && *profile != "network-wal") || (*kind == "postgresql" && *profile != "local") || (*kind == "sqlite" && *namespace != "" && *namespace != "main") {
		writeError(stderr, errors.New("queue storage arguments are invalid; upgrade requires all queue producers and workers to be stopped"))
		return 2
	}
	var database *provider.Database
	var store queueprovider.Store
	var err error
	if *kind == "sqlite" {
		value, parseErr := existingQueueSQLiteDSN(*dsn, operation == "inspect", *profile == "network-wal")
		if parseErr != nil {
			writeError(stderr, errors.New("queue storage requires an explicit existing file-backed SQLite database"))
			return 2
		}
		if operation == "inspect" {
			// The SQLite package has registered its native AutoExtensions.
			// A private read-only connection avoids application Open's journal
			// bootstrap and planner-statistics maintenance during inspection.
			standard, openErr := sqlitedriver.Open(value)
			err = openErr
			if err == nil {
				readOnly := sqlx.NewDb(standard, "sqlite3")
				readOnly.SetMaxOpenConns(1)
				readOnly.SetMaxIdleConns(1)
				defer readOnly.Close()
				store, err = internalsqlite.New().QueueStore(readOnly, nil)
			}
		} else {
			config := publicsqlite.Config{DataSourceName: value}
			switch *profile {
			case "local":
				database, err = publicsqlite.Open(ctx, config)
			case "network":
				database, err = publicsqlite.OpenNetworkFilesystem(ctx, config)
			case "network-wal":
				database, err = publicsqlite.OpenNetworkFilesystemWAL(ctx, config)
			}
			if err == nil {
				store, err = internalsqlite.New().QueueStore(database.UnsafeSQLX(), nil)
			}
		}
	} else {
		if *namespace == "" {
			*namespace = "_golem"
		}
		database, err = publicpostgresql.Open(ctx, publicpostgresql.Config{DataSourceName: *dsn, Pool: publicpostgresql.PoolConfig{MaximumOpen: 2, MaximumIdle: 2}})
		if err == nil {
			store, err = internalpostgresql.New().QueueStoreAt(database.UnsafeSQLX(), physical.PhysicalName(*namespace), nil)
		}
	}
	if database != nil {
		defer database.Close()
	}
	if err != nil {
		writeError(stderr, errors.New("queue storage connection or provider validation failed"))
		return 1
	}
	maintenance, ok := store.(queueprovider.StorageMaintenance)
	if !ok {
		writeError(stderr, errors.New("queue storage maintenance is unsupported"))
		return 1
	}
	var inspection queueprovider.StorageInspection
	if operation == "inspect" {
		inspection, err = maintenance.InspectStorage(ctx)
	} else {
		inspection, err = maintenance.UpgradeStorage(ctx)
	}
	if err != nil {
		if errors.Is(err, queueprovider.ErrStorageUnrecognized) {
			writeError(stderr, queueprovider.ErrStorageUnrecognized)
		} else {
			writeError(stderr, errors.New("queue storage "+operation+" failed; inspect storage before retrying"))
		}
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(inspection); err != nil {
		message := "queue storage inspection output could not be written"
		if operation == "upgrade" {
			message = "queue storage upgrade completed but its output could not be written"
		}
		writeError(stderr, errors.New(message))
		return 1
	}
	return 0
}

func existingQueueSQLiteDSN(value string, readOnly, exclusive bool) (string, error) {
	if !strings.HasPrefix(value, "file:") {
		value = "file:" + value
	}
	uri, err := url.Parse(value)
	if err != nil || uri.Scheme != "file" || uri.Fragment != "" || uri.Host != "" {
		return "", errors.New("invalid sqlite identity")
	}
	query, err := url.ParseQuery(uri.RawQuery)
	if err != nil || (uri.Path == "" && uri.Opaque == "") || uri.Path == ":memory:" || uri.Opaque == ":memory:" {
		return "", errors.New("invalid sqlite identity")
	}
	for key, values := range query {
		if strings.EqualFold(key, "mode") {
			if len(values) != 1 || (values[0] != "rw" && !(readOnly && values[0] == "ro")) {
				return "", errors.New("sqlite storage requires existing-file mode")
			}
			query.Del(key)
		}
	}
	query.Set("mode", "rw")
	if readOnly {
		query.Set("mode", "ro")
		query.Add("_pragma", "query_only(1)")
		if exclusive {
			query.Add("_pragma", "locking_mode(EXCLUSIVE)")
		}
	}
	uri.RawQuery = query.Encode()
	return uri.String(), nil
}
