package postgresql

import (
	"strings"

	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
)

const unicodeCollation = `pg_catalog."und-x-icu"`

func NormalizeText(expression, folding string) string {
	value := "COALESCE(" + expression + ",'')"
	if folding == fulltextcontract.FoldingDiacritics {
		value = "public.unaccent(" + value + ")"
	}
	return "regexp_replace((" + value + " COLLATE " + unicodeCollation + "),'[^[:alnum:]_]+',' ','g')"
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
