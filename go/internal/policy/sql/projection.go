package sql

import (
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// ProjectColumn renders one scalar column for a select list whose values a
// decoder reads. SQLite bytes are projected as hex text because the SQLite
// driver misreads every column that follows a zero-length BLOB in the same row;
// the decoder turns the text back into bytes. Subqueries that compare or join
// on a column keep the raw expression.
func ProjectColumn(provider ir.Provider, typ ir.TypeRef, expression string) string {
	if provider != ir.ProviderSQLite || typ.Kind() != ir.ValueBytes {
		return expression
	}
	return hexProjection(expression)
}

// ProjectStorageColumn is ProjectColumn for a column known by its physical
// storage, such as a shadow table's copy of a Bytes primary key.
func ProjectStorageColumn(storage physical.StorageKind, expression string) string {
	if storage != physical.StorageSQLiteBlob {
		return expression
	}
	return hexProjection(expression)
}

func hexProjection(expression string) string {
	// TODO: remove the hex projection when go-sqlite3 >= v0.25.0 (empty-blob column offset fix) can be adopted with a compatible sqlite-vec wasm
	return "CASE WHEN " + expression + " IS NULL THEN NULL ELSE hex(" + expression + ") END"
}
