package runtime

import "testing"

func TestQueryParserAcceptsPhrasePrefix(t *testing.T) {
	terms, err := parse(`"quick bro"* fox`)
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != 2 || terms[0].value != "quick bro" || !terms[0].phrase || !terms[0].prefix || terms[1].value != "fox" || terms[1].prefix {
		t.Fatalf("terms=%#v", terms)
	}
	for _, query := range []string{`"quick b"*`, `"b"*`, `"quick a."*`} {
		if _, err := parse(query); err == nil {
			t.Fatalf("query %q accepted a one-character final prefix lexeme", query)
		} else if reason, ok := QueryValidationReason(err); !ok || reason != "full-text prefix terms require at least two characters" {
			t.Fatalf("query %q reason=%q typed=%t", query, reason, ok)
		}
	}
	if got := compileSQLite(terms[:1], "none"); got != `"quick bro"*` {
		t.Fatalf("SQLite phrase prefix=%q", got)
	}
}
