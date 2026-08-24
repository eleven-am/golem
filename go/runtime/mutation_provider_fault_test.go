package runtime

import (
	"fmt"
	"testing"
	"unsafe"

	mutationupsert "github.com/eleven-am/golem/go/internal/mutation/upsert"
	"github.com/jackc/pgx/v5/pgconn"
	ncrucsqlite "github.com/ncruces/go-sqlite3"
	moderncsqlite "modernc.org/sqlite"
)

type moderncSQLiteErrorLayout struct {
	msg  string
	code int
}

type ncrucesSQLiteErrorLayout struct {
	str  string
	msg  string
	sql  string
	code int32
}

func moderncSQLiteError(t *testing.T, code int) *moderncsqlite.Error {
	t.Helper()
	message := fmt.Sprintf("modernc result code %d", code)
	layout := &moderncSQLiteErrorLayout{msg: message, code: code}
	err := (*moderncsqlite.Error)(unsafe.Pointer(layout))
	if err.Code() != code || err.Error() != message {
		t.Fatalf("modernc.org/sqlite error layout changed: code=%d message=%q", err.Code(), err.Error())
	}
	return err
}

func ncrucesSQLiteError(t *testing.T, code int32) *ncrucsqlite.Error {
	t.Helper()
	layout := &ncrucesSQLiteErrorLayout{str: fmt.Sprintf("ncruces result code %d", code), code: code}
	err := (*ncrucsqlite.Error)(unsafe.Pointer(layout))
	if int32(err.ExtendedCode()) != code || err.Code() != ncrucsqlite.ErrorCode(uint8(code)) {
		t.Fatalf("ncruces/go-sqlite3 error layout changed: code=%d extended=%d", err.Code(), err.ExtendedCode())
	}
	return err
}

type providerFaultCase struct {
	name     string
	err      error
	conflict bool
	unique   bool
}

func providerFaultCases(t *testing.T) []providerFaultCase {
	t.Helper()
	return []providerFaultCase{
		{name: "postgresql unique violation", err: &pgconn.PgError{Code: "23505"}, conflict: true, unique: true},
		{name: "postgresql serialization failure", err: &pgconn.PgError{Code: "40001"}, conflict: true},
		{name: "postgresql deadlock detected", err: &pgconn.PgError{Code: "40P01"}, conflict: true},
		{name: "postgresql triggered data change violation", err: &pgconn.PgError{Code: "27000"}, conflict: true},
		{name: "postgresql undefined table", err: &pgconn.PgError{Code: "42P01"}},
		{name: "postgresql not null violation", err: &pgconn.PgError{Code: "23502"}},
		{name: "ncruces constraint primarykey", err: ncrucesSQLiteError(t, 1555), conflict: true, unique: true},
		{name: "ncruces constraint unique", err: ncrucesSQLiteError(t, 2067), conflict: true, unique: true},
		{name: "ncruces busy", err: ncrucesSQLiteError(t, 5), conflict: true},
		{name: "ncruces locked", err: ncrucesSQLiteError(t, 6), conflict: true},
		{name: "ncruces busy recovery", err: ncrucesSQLiteError(t, 5|(1<<8)), conflict: true},
		{name: "ncruces locked sharedcache", err: ncrucesSQLiteError(t, 6|(1<<8)), conflict: true},
		{name: "ncruces constraint notnull", err: ncrucesSQLiteError(t, 1299)},
		{name: "ncruces readonly", err: ncrucesSQLiteError(t, 8)},
		{name: "modernc constraint primarykey", err: moderncSQLiteError(t, 1555), conflict: true, unique: true},
		{name: "modernc constraint unique", err: moderncSQLiteError(t, 2067), conflict: true, unique: true},
		{name: "modernc busy", err: moderncSQLiteError(t, 5), conflict: true},
		{name: "modernc locked", err: moderncSQLiteError(t, 6), conflict: true},
		{name: "modernc busy recovery", err: moderncSQLiteError(t, 5|(1<<8)), conflict: true},
		{name: "modernc busy snapshot", err: moderncSQLiteError(t, 5|(2<<8)), conflict: true},
		{name: "modernc locked sharedcache", err: moderncSQLiteError(t, 6|(1<<8)), conflict: true},
		{name: "modernc constraint notnull", err: moderncSQLiteError(t, 1299)},
		{name: "modernc generic constraint", err: moderncSQLiteError(t, 19)},
		{name: "modernc readonly", err: moderncSQLiteError(t, 8)},
	}
}

func TestProviderConflictClassificationAgreesAcrossMutationEntryPoints(t *testing.T) {
	for _, testCase := range providerFaultCases(t) {
		t.Run(testCase.name, func(t *testing.T) {
			scalar := scalarMutationProviderFailureKind(testCase.err) == scalarMutationConflict
			nested := mutationupsert.RetryableInterference(testCase.err)
			if scalar != nested {
				t.Fatalf("entry points disagree: scalar conflict=%t upsert interference=%t", scalar, nested)
			}
			if scalar != testCase.conflict {
				t.Fatalf("conflict=%t want=%t", scalar, testCase.conflict)
			}
			if collision := mutationupsert.UniqueCollision(testCase.err); collision != testCase.unique {
				t.Fatalf("unique collision=%t want=%t", collision, testCase.unique)
			}
		})
	}
}

type untrustedProviderFailure struct{ cause error }

func (failure *untrustedProviderFailure) Error() string        { return failure.cause.Error() }
func (failure *untrustedProviderFailure) Unwrap() error        { return failure.cause }
func (failure *untrustedProviderFailure) UntrustedRetryCause() {}

func TestUntrustedCausesAreGatedOnlyByTheUpsertRetryEntryPoint(t *testing.T) {
	untrusted := &untrustedProviderFailure{cause: &pgconn.PgError{Code: "23505"}}
	if mutationupsert.ClassifyProviderFault(untrusted) != mutationupsert.ProviderFaultUniqueCollision {
		t.Fatalf("owner classified an untrusted-wrapped unique violation as %d", mutationupsert.ClassifyProviderFault(untrusted))
	}
	if mutationupsert.RetryableInterference(untrusted) || mutationupsert.UniqueCollision(untrusted) {
		t.Fatal("upsert retry accepted an untrusted cause")
	}
	if scalarMutationProviderFailureKind(untrusted) != scalarMutationConflict {
		t.Fatal("scalar classification stopped honouring a trusted provider cause wrapped by a hook failure")
	}
}
