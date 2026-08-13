package compatibility

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMigrationGuideAuthorityProvenancePinsExactTaggedCorporaAndParser(t *testing.T) {
	const (
		wantCommit      = "efadc57d1da9b03e84c8cd746323fee3cc2f72c2"
		wantParserSHA   = "cfbd0d1b8e22084a71e02d76775346d6e828bdb1076185eaa6d8c32046ca658c"
		wantParserLines = 168
	)
	_, source, _, _ := runtime.Caller(0)
	directory := filepath.Dir(source)
	parser, err := os.ReadFile(filepath.Join(directory, "migration_guide.go"))
	if err != nil {
		t.Fatal(err)
	}
	if Digest(parser) != wantParserSHA || bytes.Count(parser, []byte{'\n'}) != wantParserLines {
		t.Fatal("migration guide parser provenance changed without review")
	}
	module := filepath.Clean(filepath.Join(directory, "..", ".."))
	if got := guideGit(t, module, "rev-parse", "go/v0.0.2^{commit}"); got != wantCommit {
		t.Fatalf("tag commit=%s", got)
	}
	for path, tree := range map[string]string{"p7": "dce564e9e3aff0b0f96ae8b3278e75d588d2f71c", "p7-event": "944c1ffbb2282b081f596b46de9218209651bf6b"} {
		if got := guideGit(t, module, "rev-parse", "go/v0.0.2:go/internal/compatibility/testdata/"+path); got != tree {
			t.Fatalf("%s tagged tree=%s", path, got)
		}
	}
	guide, err := os.ReadFile(filepath.Join(module, filepath.FromSlash(MigrationGuidePath)))
	if err != nil {
		t.Fatal(err)
	}
	if Digest(guide) != MigrationGuideSHA256 {
		t.Fatalf("guide digest=%s", Digest(guide))
	}
}

func guideGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return strings.TrimSpace(string(output))
}
