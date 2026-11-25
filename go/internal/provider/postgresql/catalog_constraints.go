package postgresql

const validatedCatalogConstraintSQL = `(con.convalidated AND COALESCE((pg_catalog.to_jsonb(con)->>'conenforced')::boolean,true))`

const catalogConstraintNeedsInspectionSQL = `(con.contype<>'n' OR NOT (` + validatedCatalogConstraintSQL + `
AND NOT con.condeferrable AND NOT con.condeferred AND NOT con.connoinherit
AND con.conislocal AND con.coninhcount=0 AND con.conparentid=0
AND con.conindid=0 AND con.confrelid=0 AND con.conbin IS NULL
AND cardinality(con.conkey)=1
AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a
WHERE a.attrelid=con.conrelid AND a.attnum=con.conkey[1]
AND a.attnum>0 AND NOT a.attisdropped AND a.attnotnull)))`
