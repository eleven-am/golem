package postgresql

import "testing"

func TestFirstStringDifferenceNamesMissingAndAddedObjects(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		expected []string
		actual   []string
		want     string
	}{
		{name: "missing middle", expected: []string{"a", "b", "c"}, actual: []string{"a", "c"}, want: "b"},
		{name: "added middle", expected: []string{"a", "c"}, actual: []string{"a", "b", "c"}, want: "b"},
		{name: "reordered", expected: []string{"a", "b"}, actual: []string{"b", "a"}, want: "a"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := firstStringDifference(testCase.expected, testCase.actual); got != testCase.want {
				t.Fatalf("firstStringDifference()=%q want %q", got, testCase.want)
			}
		})
	}
}
