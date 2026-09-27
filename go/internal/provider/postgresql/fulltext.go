package postgresql

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/physical"
)

type postgresqlFullTextObjects struct {
	table, document physical.PhysicalName
	function        physical.PhysicalName
	insert          physical.PhysicalName
	update          physical.PhysicalName
	delete          physical.PhysicalName
	truncate        physical.PhysicalName
}

func postgresqlFullTextNames(descriptor fulltextstorage.Descriptor) postgresqlFullTextObjects {
	base := string(descriptor.Storage)
	return postgresqlFullTextObjects{
		table: physical.PhysicalName(base + "_fts"), document: physical.PhysicalName(base + "_fts_document"),
		function: physical.PhysicalName(base + "_sync"), insert: physical.PhysicalName(base + "_ai"),
		update: physical.PhysicalName(base + "_au"), delete: physical.PhysicalName(base + "_ad"), truncate: physical.PhysicalName(base + "_at"),
	}
}

func renderPostgreSQLFullTextExtension(namespace physical.PhysicalName, extension physical.Extension, owner physical.PhysicalTable) ([]string, error) {
	descriptor, err := fulltextstorage.Decode(extension)
	if err != nil {
		return nil, fmt.Errorf("postgresql render full-text extension %s: %w", extension.ID, err)
	}
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
	for _, column := range owner.Columns {
		columns[column.ID] = column
	}
	if owner.PrimaryKey == nil || len(owner.PrimaryKey.Columns) == 0 {
		return nil, fmt.Errorf("postgresql render full-text extension %s: owner identity is absent", extension.ID)
	}
	identity := make([]physical.PhysicalColumn, len(owner.PrimaryKey.Columns))
	for position, field := range owner.PrimaryKey.Columns {
		column, exists := columns[field]
		if !exists || column.Nullable {
			return nil, fmt.Errorf("postgresql render full-text extension %s: owner identity is invalid", extension.ID)
		}
		identity[position] = column
	}
	fields := make([]physical.PhysicalColumn, len(descriptor.Index.Fields))
	for position, field := range descriptor.Index.Fields {
		column, exists := columns[ir.FieldID(field.ID)]
		if !exists {
			return nil, fmt.Errorf("postgresql render full-text extension %s: field %s is absent", extension.ID, field.ID)
		}
		fields[position] = column
	}
	classes, err := postgresqlFullTextClasses(descriptor.Index)
	if err != nil {
		return nil, err
	}
	names := postgresqlFullTextNames(descriptor)
	identityDefinitions := make([]string, len(identity))
	identityNames := make([]string, len(identity))
	newIdentity := make([]string, len(identity))
	oldMatch := make([]string, len(identity))
	for position, column := range identity {
		storage, storageErr := renderStorage(column.Storage)
		if storageErr != nil {
			return nil, storageErr
		}
		identityDefinitions[position] = quote(column.Name) + " " + storage + " NOT NULL"
		identityNames[position] = quote(column.Name)
		newIdentity[position] = "NEW." + quote(column.Name)
		oldMatch[position] = quote(column.Name) + "=OLD." + quote(column.Name)
	}
	vectors := make([]string, len(fields))
	for position, column := range fields {
		vectors[position] = postgresqlFullTextVector("NEW."+quote(column.Name), descriptor.Index.Folding, classes[position])
	}
	updateFields, err := fulltextstorage.UpdateColumns(extension, owner)
	if err != nil {
		return nil, err
	}
	updateColumns := make([]string, len(updateFields))
	for position, column := range updateFields {
		updateColumns[position] = quote(column.Name)
	}
	deleteOld := "DELETE FROM " + qualified(namespace, names.table) + " WHERE " + strings.Join(oldMatch, " AND ")
	insertNew := "INSERT INTO " + qualified(namespace, names.table) + " (" + strings.Join(identityNames, ",") + "," + quote("document") + ") VALUES (" + strings.Join(newIdentity, ",") + "," + strings.Join(vectors, " || ") + ")"
	clear := "DELETE FROM " + qualified(namespace, names.table)
	body := "BEGIN IF TG_OP='TRUNCATE' THEN " + clear + "; RETURN NULL; END IF; IF TG_OP='DELETE' THEN " + deleteOld + "; RETURN OLD; END IF; IF TG_OP='UPDATE' THEN " + deleteOld + "; END IF; " + insertNew + "; RETURN NEW; END"
	return []string{
		"CREATE TABLE " + qualified(namespace, names.table) + " (" + strings.Join(identityDefinitions, ", ") + ", " + quote("document") + " tsvector NOT NULL, PRIMARY KEY (" + strings.Join(identityNames, ", ") + "))",
		"CREATE INDEX " + quote(names.document) + " ON " + qualified(namespace, names.table) + " USING gin (" + quote("document") + ")",
		"CREATE FUNCTION " + qualified(namespace, names.function) + "() RETURNS trigger LANGUAGE plpgsql AS " + quoteDollar(body),
		"CREATE TRIGGER " + quote(names.insert) + " AFTER INSERT ON " + qualified(namespace, owner.Name) + " FOR EACH ROW EXECUTE FUNCTION " + qualified(namespace, names.function) + "()",
		"CREATE TRIGGER " + quote(names.update) + " AFTER UPDATE OF " + strings.Join(updateColumns, ",") + " ON " + qualified(namespace, owner.Name) + " FOR EACH ROW EXECUTE FUNCTION " + qualified(namespace, names.function) + "()",
		"CREATE TRIGGER " + quote(names.delete) + " AFTER DELETE ON " + qualified(namespace, owner.Name) + " FOR EACH ROW EXECUTE FUNCTION " + qualified(namespace, names.function) + "()",
		"CREATE TRIGGER " + quote(names.truncate) + " AFTER TRUNCATE ON " + qualified(namespace, owner.Name) + " FOR EACH STATEMENT EXECUTE FUNCTION " + qualified(namespace, names.function) + "()",
	}, nil
}

func renderPostgreSQLFullTextBackfill(namespace physical.PhysicalName, extension physical.Extension, owner physical.PhysicalTable) ([]string, error) {
	descriptor, err := fulltextstorage.Decode(extension)
	if err != nil {
		return nil, err
	}
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
	for _, column := range owner.Columns {
		columns[column.ID] = column
	}
	classes, err := postgresqlFullTextClasses(descriptor.Index)
	if err != nil {
		return nil, err
	}
	identityNames := make([]string, len(owner.PrimaryKey.Columns))
	identitySelect := make([]string, len(owner.PrimaryKey.Columns))
	for position, field := range owner.PrimaryKey.Columns {
		column := columns[field]
		identityNames[position] = quote(column.Name)
		identitySelect[position] = "o." + quote(column.Name)
	}
	vectors := make([]string, len(descriptor.Index.Fields))
	for position, field := range descriptor.Index.Fields {
		column := columns[ir.FieldID(field.ID)]
		vectors[position] = postgresqlFullTextVector("o."+quote(column.Name), descriptor.Index.Folding, classes[position])
	}
	names := postgresqlFullTextNames(descriptor)
	return []string{"INSERT INTO " + qualified(namespace, names.table) + " (" + strings.Join(identityNames, ",") + "," + quote("document") + ") SELECT " + strings.Join(identitySelect, ",") + "," + strings.Join(vectors, " || ") + " FROM " + qualified(namespace, owner.Name) + " AS o"}, nil
}

func postgresqlFullTextVector(value, folding string, class byte) string {
	value = "COALESCE(" + value + ",'')"
	if folding == fulltextcontract.FoldingDiacritics {
		value = "public.unaccent(" + value + ")"
	}
	value = "regexp_replace(" + value + ",'[^[:alnum:]_]+',' ','g')"
	return "setweight(to_tsvector('simple'," + value + "),'" + string(class) + "')"
}

func renderPostgreSQLUnaccentExtension() []string {
	guard := "BEGIN IF EXISTS (SELECT 1 FROM pg_catalog.pg_extension e JOIN pg_catalog.pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='unaccent' AND n.nspname<>'public') THEN RAISE EXCEPTION 'golem full-text search requires extension unaccent in schema public'; END IF; END"
	return []string{
		"DO " + quoteDollar(guard),
		"CREATE EXTENSION IF NOT EXISTS unaccent WITH SCHEMA public",
	}
}

func dropPostgreSQLFullTextExtension(namespace physical.PhysicalName, extension physical.Extension, owner physical.PhysicalTable) ([]string, error) {
	descriptor, err := fulltextstorage.Decode(extension)
	if err != nil {
		return nil, err
	}
	names := postgresqlFullTextNames(descriptor)
	return []string{
		"DROP TRIGGER " + quote(names.insert) + " ON " + qualified(namespace, owner.Name),
		"DROP TRIGGER " + quote(names.update) + " ON " + qualified(namespace, owner.Name),
		"DROP TRIGGER " + quote(names.delete) + " ON " + qualified(namespace, owner.Name),
		"DROP TRIGGER " + quote(names.truncate) + " ON " + qualified(namespace, owner.Name),
		"DROP FUNCTION " + qualified(namespace, names.function) + "()",
		"DROP TABLE " + qualified(namespace, names.table),
	}, nil
}

func postgresqlFullTextClasses(index fulltextcontract.Index) ([]byte, error) {
	weights := make([]float64, 0, len(index.Fields))
	seen := make(map[float64]bool)
	for _, field := range index.Fields {
		if !seen[field.Weight] {
			seen[field.Weight] = true
			weights = append(weights, field.Weight)
		}
	}
	if len(weights) > 4 {
		return nil, fmt.Errorf("postgresql full-text index supports at most four distinct weights")
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(weights)))
	classes := make(map[float64]byte, len(weights))
	for position, weight := range weights {
		classes[weight] = byte('A' + position)
	}
	result := make([]byte, len(index.Fields))
	for position, field := range index.Fields {
		result[position] = classes[field.Weight]
	}
	return result, nil
}

func quoteDollar(value string) string { return "$golem$" + value + "$golem$" }
