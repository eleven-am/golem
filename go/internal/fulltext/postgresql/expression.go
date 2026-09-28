package postgresql

import (
	"strings"

	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextfolding "github.com/eleven-am/golem/go/internal/fulltext/folding"
)

const unicodeCollation = `pg_catalog."und-x-icu"`

func NormalizeText(expression, folding string) string {
	value := "COALESCE(" + expression + ",'')"
	if folding == fulltextcontract.FoldingDiacritics {
		marks := strings.ReplaceAll(fulltextfolding.MarkCharacters(), "'", "''")
		value = "pg_catalog.normalize(pg_catalog.translate(pg_catalog.normalize(" + value + ",'NFD'),'" + marks + "',''),'NFC')"
	}
	return "pg_catalog.regexp_replace((" + value + " COLLATE " + unicodeCollation + "),'[^[:alnum:]_]+',' ','g')"
}

func PhraseQuery(expression, folding string, prefix bool) string {
	phrase := "phraseto_tsquery('simple'," + NormalizeText(expression, folding) + ")"
	if !prefix {
		return phrase
	}
	return "(CASE WHEN numnode(" + phrase + ")=0 THEN " + phrase + " ELSE to_tsquery('simple',(" + phrase + ")::text||':*') END)"
}

func JoinQueries(expressions []string) string {
	return "(" + strings.Join(expressions, " || ") + ")"
}
