package postgresql

import "testing"

func TestPGVectorRuntimeVersionFloor(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		version string
		want    bool
	}{
		{version: "0.8.0", want: true},
		{version: "0.8.6", want: true},
		{version: "0.9.0", want: true},
		{version: "1.0.0", want: true},
		{version: "0.7.4", want: false},
		{version: "0.8", want: false},
		{version: "00.8.0", want: false},
		{version: "0.08.0", want: false},
		{version: "0.8.0-rc1", want: false},
		{version: "0.8.0+local", want: false},
		{version: "", want: false},
		{version: "latest", want: false},
	} {
		t.Run(test.version, func(t *testing.T) {
			t.Parallel()
			if got := supportedPGVectorVersion(test.version); got != test.want {
				t.Fatalf("supportedPGVectorVersion(%q)=%t want=%t", test.version, got, test.want)
			}
		})
	}
}
