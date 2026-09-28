package postgresql

import (
	"testing"

	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
)

func TestPrefixQueryTargetsTheLastServerParsedLexeme(t *testing.T) {
	phrase := "phraseto_tsquery('simple',pg_catalog.regexp_replace((COALESCE($1,'') COLLATE pg_catalog.\"und-x-icu\"),'[^[:alnum:]_]+',' ','g'))"
	want := "(CASE WHEN numnode(" + phrase + ")=0 THEN " + phrase + " ELSE to_tsquery('simple',(" + phrase + ")::text||':*') END)"
	if got := PhraseQuery("$1", fulltextcontract.FoldingNone, true); got != want {
		t.Fatalf("prefix query=%q want %q", got, want)
	}
}
