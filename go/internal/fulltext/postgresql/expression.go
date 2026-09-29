package postgresql

import (
	"strings"

	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextfolding "github.com/eleven-am/golem/go/internal/fulltext/folding"
)

const unicodeCollation = `pg_catalog."und-x-icu"`

func NormalizeText(expression, folding string) string {
	return NormalizeTextFor(expression, folding, "")
}

func NormalizeTextFor(expression, folding, normalization string) string {
	value := "COALESCE(" + expression + ",'')"
	if normalization == fulltextcontract.NormalizationNFCLower {
		value = "pg_catalog.lower((" + value + " COLLATE " + unicodeCollation + "))"
		if folding != fulltextcontract.FoldingDiacritics {
			value = "pg_catalog.normalize(" + value + ",'NFC')"
		}
	}
	if folding == fulltextcontract.FoldingDiacritics {
		marks := strings.ReplaceAll(fulltextfolding.MarkCharacters(), "'", "''")
		value = "pg_catalog.normalize(pg_catalog.translate(pg_catalog.normalize(" + value + ",'NFD'),'" + marks + "',''),'NFC')"
	}
	return "pg_catalog.regexp_replace((" + value + " COLLATE " + unicodeCollation + "),'[^[:alnum:]_]+',' ','g')"
}

func PhraseQuery(expression, folding string, prefix bool) string {
	return PhraseQueryFor(expression, folding, "", prefix)
}

func PhraseQueryFor(expression, folding, normalization string, prefix bool) string {
	phrase := "phraseto_tsquery('simple'," + NormalizeTextFor(expression, folding, normalization) + ")"
	if !prefix {
		return phrase
	}
	return "(CASE WHEN numnode(" + phrase + ")=0 THEN " + phrase + " ELSE to_tsquery('simple',(" + phrase + ")::text||':*') END)"
}

func JoinQueries(expressions []string) string {
	return "(" + strings.Join(expressions, " || ") + ")"
}
