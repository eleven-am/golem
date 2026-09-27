package sqlite

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextfolding "github.com/eleven-am/golem/go/internal/fulltext/folding"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/physical"
)

const fullTextFoldFunction = "golem_fulltext_fold"

func sqliteFullTextFold(arguments []driver.Value) (driver.Value, error) {
	if len(arguments) != 1 {
		return nil, fmt.Errorf("%s: arity", fullTextFoldFunction)
	}
	if arguments[0] == nil {
		return nil, nil
	}
	value, ok := arguments[0].(string)
	if !ok {
		return nil, fmt.Errorf("%s: expected TEXT", fullTextFoldFunction)
	}
	return fulltextfolding.Diacritics(value), nil
}

type sqliteFullTextObjects struct {
	keys, index physical.PhysicalName
	insert      physical.PhysicalName
	update      physical.PhysicalName
	delete      physical.PhysicalName
}

func sqliteFullTextNames(descriptor fulltextstorage.Descriptor) sqliteFullTextObjects {
	base := string(descriptor.Storage)
	return sqliteFullTextObjects{
		keys: physical.PhysicalName(base + "_keys"), index: physical.PhysicalName(base + "_fts"),
		insert: physical.PhysicalName(base + "_ai"), update: physical.PhysicalName(base + "_au"), delete: physical.PhysicalName(base + "_ad"),
	}
}

func renderFullTextExtension(extension physical.Extension, owner physical.PhysicalTable) ([]string, error) {
	descriptor, err := fulltextstorage.Decode(extension)
	if err != nil {
		return nil, fmt.Errorf("sqlite render full-text extension %s: %w", extension.ID, err)
	}
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
	for _, column := range owner.Columns {
		columns[column.ID] = column
	}
	if owner.PrimaryKey == nil || len(owner.PrimaryKey.Columns) == 0 {
		return nil, fmt.Errorf("sqlite render full-text extension %s: owner identity is absent", extension.ID)
	}
	identity := make([]physical.PhysicalColumn, len(owner.PrimaryKey.Columns))
	for position, field := range owner.PrimaryKey.Columns {
		column, exists := columns[field]
		if !exists || column.Nullable {
			return nil, fmt.Errorf("sqlite render full-text extension %s: owner identity is invalid", extension.ID)
		}
		identity[position] = column
	}
	fields := make([]physical.PhysicalColumn, len(descriptor.Index.Fields))
	for position, field := range descriptor.Index.Fields {
		column, exists := columns[ir.FieldID(field.ID)]
		if !exists {
			return nil, fmt.Errorf("sqlite render full-text extension %s: field %s is absent", extension.ID, field.ID)
		}
		fields[position] = column
	}
	names := sqliteFullTextNames(descriptor)
	keyColumns := make([]string, len(identity))
	keyNames := make([]string, len(identity))
	newIdentity := make([]string, len(identity))
	oldIdentity := make([]string, len(identity))
	identityMatchOld := make([]string, len(identity))
	identityMatchNew := make([]string, len(identity))
	for position, column := range identity {
		keyColumns[position] = quote(column.Name) + " " + renderStorage(column.Storage) + " NOT NULL"
		keyNames[position] = quote(column.Name)
		newIdentity[position] = "NEW." + quote(column.Name)
		oldIdentity[position] = "OLD." + quote(column.Name)
		identityMatchOld[position] = quote(column.Name) + "=OLD." + quote(column.Name)
		identityMatchNew[position] = quote(column.Name) + "=NEW." + quote(column.Name)
	}
	fieldNames := make([]string, len(fields))
	newFields := make([]string, len(fields))
	for position, column := range fields {
		fieldNames[position] = quote(sqliteFullTextFieldName(position))
		newFields[position] = "COALESCE(NEW." + quote(column.Name) + ",'')"
		if descriptor.Index.Folding == fulltextcontract.FoldingDiacritics {
			newFields[position] = fullTextFoldFunction + "(" + newFields[position] + ")"
		}
	}
	tokenizer := "unicode61 remove_diacritics 0"
	options := []string{"content=''", "contentless_delete=1", "tokenize=" + quoteLiteral(tokenizer)}
	if len(descriptor.Index.Prefix) != 0 {
		parts := make([]string, len(descriptor.Index.Prefix))
		for position, length := range descriptor.Index.Prefix {
			parts[position] = strconv.Itoa(int(length))
		}
		options = append(options, "prefix="+quoteLiteral(strings.Join(parts, " ")))
	}
	docidOld := "(SELECT " + quote("docid") + " FROM " + quote(names.keys) + " WHERE " + strings.Join(identityMatchOld, " AND ") + ")"
	docidNew := "(SELECT " + quote("docid") + " FROM " + quote(names.keys) + " WHERE " + strings.Join(identityMatchNew, " AND ") + ")"
	insertIndex := "INSERT INTO " + quote(names.index) + " (rowid," + strings.Join(fieldNames, ",") + ") VALUES (" + docidNew + "," + strings.Join(newFields, ",") + ")"
	updateFields, err := fulltextstorage.UpdateColumns(extension, owner)
	if err != nil {
		return nil, err
	}
	updateColumns := make([]string, len(updateFields))
	for position, column := range updateFields {
		updateColumns[position] = quote(column.Name)
	}
	return []string{
		"CREATE TABLE " + quote(names.keys) + " (" + quote("docid") + " INTEGER PRIMARY KEY, " + strings.Join(keyColumns, ", ") + ", UNIQUE (" + strings.Join(keyNames, ", ") + ")) STRICT",
		"CREATE VIRTUAL TABLE " + quote(names.index) + " USING fts5(" + strings.Join(fieldNames, ",") + "," + strings.Join(options, ",") + ")",
		"CREATE TRIGGER " + quote(names.insert) + " AFTER INSERT ON " + quote(owner.Name) + " BEGIN INSERT INTO " + quote(names.keys) + " (" + strings.Join(keyNames, ",") + ") VALUES (" + strings.Join(newIdentity, ",") + "); " + insertIndex + "; END",
		"CREATE TRIGGER " + quote(names.update) + " AFTER UPDATE OF " + strings.Join(updateColumns, ",") + " ON " + quote(owner.Name) + " BEGIN DELETE FROM " + quote(names.index) + " WHERE rowid=" + docidOld + "; UPDATE " + quote(names.keys) + " SET (" + strings.Join(keyNames, ",") + ")=(" + strings.Join(newIdentity, ",") + ") WHERE " + strings.Join(identityMatchOld, " AND ") + "; " + insertIndex + "; END",
		"CREATE TRIGGER " + quote(names.delete) + " AFTER DELETE ON " + quote(owner.Name) + " BEGIN DELETE FROM " + quote(names.index) + " WHERE rowid=" + docidOld + "; DELETE FROM " + quote(names.keys) + " WHERE " + strings.Join(identityMatchOld, " AND ") + "; END",
	}, nil
}

func renderFullTextBackfill(extension physical.Extension, owner physical.PhysicalTable) ([]string, error) {
	descriptor, err := fulltextstorage.Decode(extension)
	if err != nil {
		return nil, err
	}
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
	for _, column := range owner.Columns {
		columns[column.ID] = column
	}
	names := sqliteFullTextNames(descriptor)
	identityNames := make([]string, len(owner.PrimaryKey.Columns))
	identitySelect := make([]string, len(owner.PrimaryKey.Columns))
	identityJoin := make([]string, len(owner.PrimaryKey.Columns))
	for position, field := range owner.PrimaryKey.Columns {
		column := columns[field]
		identityNames[position] = quote(column.Name)
		identitySelect[position] = "o." + quote(column.Name)
		identityJoin[position] = "k." + quote(column.Name) + "=o." + quote(column.Name)
	}
	fieldNames := make([]string, len(descriptor.Index.Fields))
	fieldSelect := make([]string, len(descriptor.Index.Fields))
	for position, field := range descriptor.Index.Fields {
		column := columns[ir.FieldID(field.ID)]
		fieldNames[position] = quote(sqliteFullTextFieldName(position))
		fieldSelect[position] = "COALESCE(o." + quote(column.Name) + ",'')"
		if descriptor.Index.Folding == fulltextcontract.FoldingDiacritics {
			fieldSelect[position] = fullTextFoldFunction + "(" + fieldSelect[position] + ")"
		}
	}
	return []string{
		"INSERT INTO " + quote(names.keys) + " (" + strings.Join(identityNames, ",") + ") SELECT " + strings.Join(identitySelect, ",") + " FROM " + quote(owner.Name) + " AS o",
		"INSERT INTO " + quote(names.index) + " (rowid," + strings.Join(fieldNames, ",") + ") SELECT k." + quote("docid") + "," + strings.Join(fieldSelect, ",") + " FROM " + quote(owner.Name) + " AS o JOIN " + quote(names.keys) + " AS k ON " + strings.Join(identityJoin, " AND "),
	}, nil
}

func sqliteFullTextFieldName(position int) physical.PhysicalName {
	return physical.PhysicalName("_golem_field_" + strconv.Itoa(position))
}

func dropFullTextExtension(extension physical.Extension) ([]string, error) {
	descriptor, err := fulltextstorage.Decode(extension)
	if err != nil {
		return nil, err
	}
	names := sqliteFullTextNames(descriptor)
	return []string{
		"DROP TRIGGER " + quote(names.insert),
		"DROP TRIGGER " + quote(names.update),
		"DROP TRIGGER " + quote(names.delete),
		"DROP TABLE " + quote(names.index),
		"DROP TABLE " + quote(names.keys),
	}, nil
}
