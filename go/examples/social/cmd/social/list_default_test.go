package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestRelationListDefaultMatchesAdvertisedDefault(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(socialHostRoot(t), "social", "zz_golem_graphql.schema.graphqls"))
	if err != nil {
		t.Fatal(err)
	}
	typeLine := regexp.MustCompile(`(?m)^type (\w+) \{$`)
	listLine := regexp.MustCompile(`(?m)^  (\w+)\(.*take: Int = (\d+)\): \[(\w+)!\]!?$`)
	rootDefaults := map[string]string{}
	type relation struct{ owner, field, element, advertised string }
	var relations []relation
	owner := ""
	for _, line := range regexp.MustCompile(`\n`).Split(string(source), -1) {
		if matched := typeLine.FindStringSubmatch(line); matched != nil {
			owner = matched[1]
			continue
		}
		if matched := listLine.FindStringSubmatch(line); matched != nil {
			if owner == "Query" {
				rootDefaults[matched[3]] = matched[2]
			} else {
				relations = append(relations, relation{owner, matched[1], matched[3], matched[2]})
			}
		}
	}
	if len(rootDefaults) == 0 || len(relations) == 0 {
		t.Fatalf("no list defaults found: roots=%v relations=%d", rootDefaults, len(relations))
	}
	for _, item := range relations {
		want, ok := rootDefaults[item.element]
		if !ok {
			t.Fatalf("%s.%s lists %s, which has no findMany root to compare with", item.owner, item.field, item.element)
		}
		if item.advertised != want {
			t.Errorf("%s.%s advertises take %s but %s lists default to %s", item.owner, item.field, item.advertised, item.element, want)
		}
	}
}
