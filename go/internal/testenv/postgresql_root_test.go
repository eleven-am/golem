package testenv

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPrivatePostgreSQLRejectsForeignEndpointsBeforeConnecting(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "socket")
	for _, test := range []struct {
		name, root, dsn string
	}{
		{"missing-root", "", "host=" + socket},
		{"relative-root", "relative", "host=" + socket},
		{"missing-dsn", root, ""},
		{"tcp", root, "host=127.0.0.1"},
		{"other-socket", root, "host=" + filepath.Join(root, "other")},
		{"tcp-fallback", root, "host=" + socket + ",127.0.0.1"},
		{"other-port-fallback", root, "host=" + socket + "," + socket + " port=5541,5542"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := rootPostgreSQLDSN(test.root, test.dsn); err == nil {
				t.Fatal("accepted a missing or foreign private cluster endpoint")
			}
			// This call must fail on arguments; there is no server to contact.
			if err := ValidatePostgreSQLRoot(context.Background(), test.root, test.dsn); err == nil {
				t.Fatal("ownership verification accepted an unsafe endpoint")
			}
		})
	}
}

func TestPrivatePostgreSQLPinsDatabaseReplacementAndSocket(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "socket")
	address := url.URL{Scheme: "postgres", Host: "localhost", User: url.User("fixture"), Path: "/ignored"}
	address.RawQuery = url.Values{"host": {socket}, "port": {"5541"}, "dbname": {"admin"}}.Encode()
	canonical, err := rootPostgreSQLDSN(root, address.String())
	if err != nil {
		t.Fatal(err)
	}
	// A retained dbname query parameter would silently override the newly
	// allocated path and send migration writes back into the admin database.
	created := replaceDisposableDatabase(canonical, "owned_database")
	config, err := pgx.ParseConfig(created)
	if err != nil || config.Database != "owned_database" || config.Host != socket || config.Port != 5541 || config.User != "fixture" || len(config.Fallbacks) != 0 {
		t.Fatalf("disposable DSN did not preserve the checked endpoint/database: %v", err)
	}
}
