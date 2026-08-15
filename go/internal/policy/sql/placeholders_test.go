package sql

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

func legacyMutationRebase(text string, offset int, provider ir.Provider) string {
	if offset == 0 {
		return text
	}
	marker := byte('$')
	if provider == ir.ProviderSQLite {
		marker = '?'
	}
	var result strings.Builder
	for index := 0; index < len(text); {
		if text[index] != marker || index+1 >= len(text) || text[index+1] < '0' || text[index+1] > '9' {
			result.WriteByte(text[index])
			index++
			continue
		}
		end := index + 1
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		position, _ := strconv.Atoi(text[index+1 : end])
		result.WriteByte(marker)
		result.WriteString(strconv.Itoa(position + offset))
		index = end
	}
	return result.String()
}

func legacyNestedRebase(text string, offset int, provider ir.Provider) string {
	if offset == 0 {
		return text
	}
	marker := byte('$')
	if provider == ir.ProviderSQLite {
		marker = '?'
	}
	var output strings.Builder
	for index := 0; index < len(text); {
		if text[index] != marker || index+1 >= len(text) || text[index+1] < '0' || text[index+1] > '9' {
			output.WriteByte(text[index])
			index++
			continue
		}
		end := index + 1
		position := 0
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			position = position*10 + int(text[end]-'0')
			end++
		}
		output.WriteByte(marker)
		output.WriteString(fmt.Sprintf("%d", position+offset))
		index = end
	}
	return output.String()
}

func legacyReadRebase(sql string, offset int, provider ir.Provider) string {
	if offset == 0 {
		return sql
	}
	marker := byte('$')
	if provider == ir.ProviderSQLite {
		marker = '?'
	} else if provider != ir.ProviderPostgreSQL {
		return sql
	}
	var result strings.Builder
	for index := 0; index < len(sql); {
		if sql[index] != marker || index+1 >= len(sql) || sql[index+1] < '0' || sql[index+1] > '9' {
			result.WriteByte(sql[index])
			index++
			continue
		}
		end := index + 1
		for end < len(sql) && sql[end] >= '0' && sql[end] <= '9' {
			end++
		}
		value, _ := strconv.Atoi(sql[index+1 : end])
		result.WriteByte(marker)
		result.WriteString(strconv.Itoa(value + offset))
		index = end
	}
	return result.String()
}

func rebaseCorpus() []string {
	return []string{
		"",
		"SELECT 1",
		`"t"."id" = $1`,
		`"t"."id" = ?1`,
		`"t"."a" = $1 AND "t"."b" = $2 OR "t"."c" IN ($3, $4, $10, $11)`,
		`"t"."a" = ?1 AND "t"."b" = ?2 OR "t"."c" IN (?3, ?4, ?10, ?11)`,
		`$9 $10 $99 $100`,
		`?9 ?10 ?99 ?100`,
		`'literal $1 inside text' AND "col" = $1`,
		`'literal ?1 inside text' AND "col" = ?1`,
		`$`,
		`?`,
		`$x $ $1`,
		`?x ? ?1`,
		`json_extract("t"."payload", '$.a') = $1`,
		`json_extract("t"."payload", '$.a') = ?1`,
		`CAST($1 AS TEXT) COLLATE "C" > $2`,
		`(SELECT count(*) FROM "u" WHERE "u"."x" = $7) AND "t"."y" = $8`,
		`(SELECT count(*) FROM "u" WHERE "u"."x" = ?7) AND "t"."y" = ?8`,
	}
}

func rebaseOffsets() []int {
	return []int{0, 1, 2, 5, 17, 1000}
}

func TestRebasePlaceholdersMatchesEveryReplacedImplementation(t *testing.T) {
	for _, provider := range []ir.Provider{ir.ProviderSQLite, ir.ProviderPostgreSQL} {
		for _, offset := range rebaseOffsets() {
			for _, text := range rebaseCorpus() {
				got := RebasePlaceholders(text, offset, provider)
				if want := legacyMutationRebase(text, offset, provider); got != want {
					t.Fatalf("provider %d offset %d text %q: got %q want %q (mutation/batch, mutation/sql)", provider, offset, text, got, want)
				}
				if want := legacyNestedRebase(text, offset, provider); got != want {
					t.Fatalf("provider %d offset %d text %q: got %q want %q (mutation/nested)", provider, offset, text, got, want)
				}
				if want := legacyReadRebase(text, offset, provider); got != want {
					t.Fatalf("provider %d offset %d text %q: got %q want %q (read/sql)", provider, offset, text, got, want)
				}
			}
		}
	}
}

func TestRebasePlaceholdersShiftsOnlyItsOwnProviderMarker(t *testing.T) {
	cases := []struct {
		provider ir.Provider
		text     string
		offset   int
		want     string
	}{
		{ir.ProviderPostgreSQL, `"a" = $1 AND "b" = ?1`, 3, `"a" = $4 AND "b" = ?1`},
		{ir.ProviderSQLite, `"a" = $1 AND "b" = ?1`, 3, `"a" = $1 AND "b" = ?4`},
		{ir.ProviderPostgreSQL, `$1 $2 $3`, 0, `$1 $2 $3`},
		{ir.ProviderPostgreSQL, `$10`, 5, `$15`},
		{ir.ProviderSQLite, `?10`, 5, `?15`},
	}
	for _, testCase := range cases {
		if got := RebasePlaceholders(testCase.text, testCase.offset, testCase.provider); got != testCase.want {
			t.Fatalf("provider %d offset %d text %q: got %q want %q", testCase.provider, testCase.offset, testCase.text, got, testCase.want)
		}
	}
}

func TestRebasePlaceholdersRejectsProvidersWithUnknownPlaceholderSyntax(t *testing.T) {
	if _, err := NewCapabilityProof(ir.Provider(3), [32]byte{1}); err == nil {
		t.Fatal("capability proof accepted a provider outside SQLite and PostgreSQL")
	}
	if got := RebasePlaceholders(`"a" = $1`, 4, ir.Provider(3)); got != `"a" = $1` {
		t.Fatalf("unknown provider rewrote placeholders: %q", got)
	}
	if got := RebasePlaceholders(`"a" = $1`, 4, ir.Provider(0)); got != `"a" = $1` {
		t.Fatalf("zero provider rewrote placeholders: %q", got)
	}
}
