package handle_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	"github.com/eleven-am/golem/go/provider"
	"github.com/jmoiron/sqlx"
)

func TestP8DatabaseCloseFailureIsStableClosedAndRedacted(t *testing.T) {
	const canary = "public-raw-driver-close-canary"
	raw := sql.OpenDB(publicCloseFailureConnector{message: canary})
	if err := raw.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	internal := providerhandle.AdoptUnverifiedForTest(sqlx.NewDb(raw, "close-failure"), providerhandle.TestMetadata{
		Provider:    golem.SQLite,
		Version:     providerhandle.Version{Major: 3, Minor: 38},
		MaximumOpen: 1,
		MaximumIdle: 1,
	})
	database := (*provider.Database)(internal)

	first := database.Close()
	second := database.Close()
	if first == nil || first != second {
		t.Fatalf("close errors are not stable: first=%v second=%v", first, second)
	}
	if strings.Contains(first.Error(), canary) || first.Error() != "provider close failed" {
		t.Fatalf("public close failure was not redacted: %v", first)
	}
	if code, ok := provider.CodeOf(first); !ok || code != provider.CodeClose {
		t.Fatalf("public close code=%q known=%t", code, ok)
	}
	if database.UnsafeSQLX() != nil {
		t.Fatal("failed close retained public raw-pool access")
	}
}

type publicCloseFailureConnector struct{ message string }

func (connector publicCloseFailureConnector) Connect(context.Context) (driver.Conn, error) {
	return &publicCloseFailureConnection{message: connector.message}, nil
}
func (connector publicCloseFailureConnector) Driver() driver.Driver {
	return publicCloseFailureDriver{connector: connector}
}

type publicCloseFailureDriver struct{ connector publicCloseFailureConnector }

func (driver publicCloseFailureDriver) Open(string) (driver.Conn, error) {
	return driver.connector.Connect(context.Background())
}

type publicCloseFailureConnection struct{ message string }

func (*publicCloseFailureConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (connection *publicCloseFailureConnection) Close() error { return errors.New(connection.message) }
func (*publicCloseFailureConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("begin unsupported")
}
func (*publicCloseFailureConnection) Ping(context.Context) error { return nil }

var _ driver.Connector = publicCloseFailureConnector{}
var _ driver.Pinger = (*publicCloseFailureConnection)(nil)
