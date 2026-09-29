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

func TestQueryParserRejectsTextGluedToAPhrasePrefix(t *testing.T) {
	for _, query := range []string{`"foo"*bar`, `"quick bro"*fox tail`, `"foo"**`, `"foo"*"bar"`} {
		_, err := parse(query)
		if err == nil {
			t.Fatalf("query %q accepted text directly after a phrase prefix", query)
		}
		if reason, ok := QueryValidationReason(err); !ok || reason != "full-text phrase prefix must be followed by whitespace or the end of the query" {
			t.Fatalf("query %q reason=%q typed=%t", query, reason, ok)
		}
	}
	for _, query := range []string{`"foo"*`, `"foo"* bar`, "\"foo\"* bar", `"user@example.com"*`} {
		if _, err := parse(query); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
	}
}
