package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compatibility"
)

func TestP8ExecutableGoV002ToV010MigrationGuide(t *testing.T) {
	root := commandModuleRoot(t)
	guidePath := filepath.Join(root, filepath.FromSlash(compatibility.MigrationGuidePath))
	encoded, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatal(err)
	}
	guide, err := compatibility.ParseMigrationGuide(encoded, compatibility.MigrationGuideSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := compatibility.ValidateMigrationGuideTransition(guide, compatibility.MigrationGuideAuthority{
		Path: compatibility.MigrationGuidePath, SHA256: compatibility.MigrationGuideSHA256, FromTag: "go/v0.0.2", ToVersion: "v0.1.0",
	}, "go/v0.0.2", "efadc57d1da9b03e84c8cd746323fee3cc2f72c2", "go/v0.1.0", "v0.1.0", []string{"migrate.database", "migration-guide.execute", "regenerate.generated"}); err != nil {
		t.Fatal(err)
	}

	for _, corpus := range guide.Corpora {
		actual := p8GitOutput(t, root, "rev-parse", "HEAD:go/"+corpus.Path)
		historical := p8GitOutput(t, root, "rev-parse", guide.From.Tag+":go/"+corpus.Path)
		if actual != corpus.GitTree || historical != corpus.GitTree || actual != historical {
			t.Fatalf("guide corpus %s current/tag/authority=%s/%s/%s", corpus.Path, actual, historical, corpus.GitTree)
		}
		p8GitRun(t, root, "diff", "--quiet", guide.From.Tag, "--", "go/"+corpus.Path)
	}
	if !reflect.DeepEqual(guide.Evidence, []compatibility.MigrationGuideEvidence{{
		Identity: "github.com/eleven-am/golem/go/cmd/golem:TestP8ExecutableGoV002ToV010MigrationGuide",
		Command:  "go test ./cmd/golem -run '^TestP8ExecutableGoV002ToV010MigrationGuide$' -count=1",
	}}) {
		t.Fatalf("guide evidence=%#v", guide.Evidence)
	}

	t.Run("sqlite-data-history", p8RunP7ToReleaseUpgradeSQLite)
	t.Run("postgresql-data-history", p8RunP7ToReleaseUpgradePostgreSQLProfiles)
	t.Run("authorization-events", p8RunUpgradePreservesAuthorizationMigrationChainAndPendingEvents)
}

func p8GitRun(t *testing.T, moduleRoot string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func p8GitOutput(t *testing.T, moduleRoot string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = moduleRoot
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return string(bytes.TrimSpace(output))
}
