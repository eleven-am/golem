package rejection_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	"github.com/eleven-am/golem/go/internal/migration"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/runtime"
	"github.com/jmoiron/sqlx"
)

type forgedModel struct{}

func TestP8ForgedCapabilityAndGeneratedIdentityRejection(t *testing.T) {
	t.Run("sealed provider handle", func(t *testing.T) {
		var forged provider.Database
		if forged.Provider() != "" || forged.UnsafeSQLX() != nil || forged.Capabilities().Provider() != "" {
			t.Fatal("zero public database value acquired a provider capability")
		}
		if features := forged.Capabilities().Features(); len(features) != 0 {
			t.Fatalf("zero public database value acquired features: %#v", features)
		}
	})

	t.Run("foreign generated field", func(t *testing.T) {
		model, foreign := golem.ModelID{1}, golem.ModelID{2}
		field := golem.GeneratedTextField[forgedModel, string](golem.FieldID{3})
		input := golem.GeneratedCreateInput(model, golem.GeneratedCreateFieldValue(foreign, field, "canary"))
		_, err := golem.RuntimeFreezeCreateInput(input)
		assertBadGeneratedIdentity(t, err, "create")
	})

	t.Run("incomplete generated selector", func(t *testing.T) {
		model := golem.ModelID{5}
		selector := golem.GeneratedUniqueSelectorValue[forgedModel](
			model,
			golem.KeyID{7},
			golem.GeneratedSelectorComponent(golem.FieldID{}, int64(8)),
		)
		_, err := golem.RuntimeFreezeMutationTarget(selector)
		assertBadGeneratedIdentity(t, err, "mutation")
	})

	t.Run("mixed generated package stamp", func(t *testing.T) {
		expected, foreign := golem.SchemaDigest{1}, golem.SchemaDigest{2}
		_, err := golem.GeneratedApplicationDescriptors(
			expected,
			golem.GeneratedStampedPackageDescriptors(expected),
			golem.GeneratedStampedPackageDescriptors(foreign),
		)
		var mismatch *golem.GenerationDigestError
		if !errors.As(err, &mismatch) || mismatch.PackageIndex != 1 || mismatch.Expected != expected || mismatch.Actual != foreign {
			t.Fatalf("mixed generation error = %#v", err)
		}
	})
}

func assertBadGeneratedIdentity(t *testing.T, err error, operation string) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Operation != operation {
		t.Fatalf("generated identity error = %#v", err)
	}
	if strings.Contains(err.Error(), "canary") {
		t.Fatalf("generated identity error disclosed an input value: %v", err)
	}
}

func TestP8UnsupportedPersistedVersionNeverReinterpreted(t *testing.T) {
	t.Run("fact", func(t *testing.T) {
		encoded := append([]byte("GOLEMFACT"), 0, 0)
		binary.BigEndian.PutUint16(encoded[len("GOLEMFACT"):], 0xffff)
		_, err := mutationfact.DecodeWithResolver(encoded, neverResolveFactSchema{})
		var failure *mutationfact.CodecError
		if !errors.As(err, &failure) || failure.Detail != "unsupported fact version 65535" {
			t.Fatalf("unsupported fact version error = %#v", err)
		}
	})

	t.Run("event", func(t *testing.T) {
		encoded := make([]byte, 64)
		copy(encoded, "GOLEMEVENT")
		binary.BigEndian.PutUint16(encoded[len("GOLEMEVENT"):], 0xffff)
		_, err := eventcodec.Decode(encoded, neverResolveFactSchema{}, eventcodec.Limits{})
		var failure *eventcodec.Error
		if !errors.As(err, &failure) || failure.Detail != "unsupported event version 65535" {
			t.Fatalf("unsupported event version error = %#v", err)
		}
	})

	t.Run("reviewed migration", func(t *testing.T) {
		manifest := migration.Manifest{
			FormatVersion:    migration.ManifestFormatVersion + 1,
			CanonicalVersion: migration.ManifestCanonicalVersion,
			HashAlgorithm:    "sha256",
			GeneratorVersion: "p8-rejection-oracle",
		}
		err := migration.VerifyEmbeddedManifest(manifest)
		if err == nil || err.Error() != "unsupported migration manifest format 2" {
			t.Fatalf("unsupported migration version error = %#v", err)
		}
	})
}

type neverResolveFactSchema struct{}

func (neverResolveFactSchema) ResolveFactSchema(mutationfact.SchemaReference) (*policyschema.Registry, golem.SchemaDigest, bool) {
	panic("unsupported persisted version reached schema resolution")
}

type countingConnector struct {
	statements atomic.Int64
}

func (connector *countingConnector) Connect(context.Context) (driver.Conn, error) {
	return &countingConnection{connector: connector}, nil
}

func (*countingConnector) Driver() driver.Driver { return countingDriver{} }

type countingDriver struct{}

func (countingDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type countingConnection struct {
	connector *countingConnector
}

func (*countingConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}

func (*countingConnection) Close() error              { return nil }
func (*countingConnection) Begin() (driver.Tx, error) { return nil, errors.New("begin unsupported") }
func (connection *countingConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	connection.connector.statements.Add(1)
	return nil, errors.New("unexpected database execution")
}
func (connection *countingConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	connection.connector.statements.Add(1)
	return nil, errors.New("unexpected database query")
}
func (*countingConnection) Ping(context.Context) error { return nil }

type poisonCDC struct {
	identityCalls atomic.Int64
	runCalls      atomic.Int64
}

func (adapter *poisonCDC) Identity() events.CDCIdentity {
	adapter.identityCalls.Add(1)
	return events.CDCIdentity{Name: "p8-poison", Version: "1", Provider: golem.SQLite}
}
func (*poisonCDC) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	panic("preflight rejection reached CDC correlation")
}
func (adapter *poisonCDC) Run(context.Context, events.CDCEmitter) error {
	adapter.runCalls.Add(1)
	return errors.New("preflight rejection started a CDC worker")
}

func TestP8RejectionTouchesNoDatabaseOrWorkerWhenPreflightCanDecide(t *testing.T) {
	connector := &countingConnector{}
	raw := sql.OpenDB(connector)
	t.Cleanup(func() { _ = raw.Close() })
	internal := providerhandle.AdoptUnverifiedForTest(sqlx.NewDb(raw, "p8-rejection"), providerhandle.TestMetadata{
		Provider: golem.SQLite, Version: providerhandle.Version{Major: 3, Minor: 38},
		MaximumOpen: 1, MaximumIdle: 1,
	})
	database := (*provider.Database)(internal)
	adapter := &poisonCDC{}

	_, err := runtime.Open(context.Background(), runtime.Config[struct{}, struct{}]{
		Database: database,
		// A zero bundle is a forged generated artifact. Bundle validation is
		// entirely portable and must precede database and worker activity.
		Bundle:           golem.SchemaBundle{},
		CDCAdapters:      []events.CDCAdapter{adapter},
		ResolvePrincipal: func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "P3_RUNTIME_SCHEMA") {
		t.Fatalf("forged bundle error = %v", err)
	}
	if got := connector.statements.Load(); got != 0 {
		t.Fatalf("preflight rejection executed %d database statements", got)
	}
	if got := adapter.identityCalls.Load(); got != 0 {
		t.Fatalf("preflight rejection inspected CDC identity %d times", got)
	}
	if got := adapter.runCalls.Load(); got != 0 {
		t.Fatalf("preflight rejection started CDC worker %d times", got)
	}
}

var (
	_ driver.Connector      = (*countingConnector)(nil)
	_ driver.ExecerContext  = (*countingConnection)(nil)
	_ driver.QueryerContext = (*countingConnection)(nil)
	_ driver.Pinger         = (*countingConnection)(nil)
	_ events.CDCAdapter     = (*poisonCDC)(nil)
	_ io.Closer             = (*countingConnection)(nil)
)
