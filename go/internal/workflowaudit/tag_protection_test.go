package workflowaudit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8TagProtectionVerifierRequiresExactSHAAndImmutableSignedRuleset(t *testing.T) {
	checkout, sha := tagProtectionRepository(t)
	token := "protected-test-token"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("authorization channel missing")
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/repos/eleven-am/golem/rulesets":
			_ = json.NewEncoder(response).Encode([]rulesetSummary{{ID: 71, Target: "tag", Enforcement: "active"}})
		case "/repos/eleven-am/golem/rulesets/71":
			_, _ = response.Write([]byte(`{"id":71,"target":"tag","enforcement":"active","conditions":{"ref_name":{"include":["refs/tags/go/v*.*.*"],"exclude":[]}},"rules":[{"type":"deletion"},{"type":"non_fast_forward"},{"type":"required_signatures"}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	config := TagProtectionConfig{Repository: "eleven-am/golem", Ref: "refs/tags/go/v1.2.3", SHA: sha, Checkout: checkout, Token: token, APIBaseURL: server.URL, Client: server.Client()}
	evidence, err := VerifyTagProtection(context.Background(), config)
	if err != nil || evidence.Status != "PASS" || evidence.RulesetID != 71 || evidence.FormatVersion != 1 {
		t.Fatalf("evidence=%#v err=%v", evidence, err)
	}
	config.SHA = strings.Repeat("0", 40)
	if _, err := VerifyTagProtection(context.Background(), config); err == nil || err.Error() != "P8_TAG_PROTECTION_SHA_MISMATCH" {
		t.Fatalf("SHA mismatch err=%v", err)
	}
}

func TestP8TagProtectionVerifierRejectsMissingMutableOrExcludedRule(t *testing.T) {
	checkout, sha := tagProtectionRepository(t)
	for _, scenario := range []struct {
		name, detail string
	}{
		{"missing-signature", `{"id":2,"target":"tag","enforcement":"active","conditions":{"ref_name":{"include":["~ALL"]}},"rules":[{"type":"deletion"},{"type":"non_fast_forward"}]}`},
		{"excluded", `{"id":2,"target":"tag","enforcement":"active","conditions":{"ref_name":{"include":["~ALL"],"exclude":["refs/tags/go/v1.2.3"]}},"rules":[{"type":"deletion"},{"type":"non_fast_forward"},{"type":"required_signatures"}]}`},
		{"evaluate-only", `{"id":2,"target":"tag","enforcement":"evaluate","conditions":{"ref_name":{"include":["~ALL"]}},"rules":[{"type":"deletion"},{"type":"non_fast_forward"},{"type":"required_signatures"}]}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(request.URL.Path, "/rulesets") {
					_ = json.NewEncoder(response).Encode([]rulesetSummary{{ID: 2, Target: "tag", Enforcement: "active"}})
					return
				}
				_, _ = response.Write([]byte(scenario.detail))
			}))
			defer server.Close()
			_, err := VerifyTagProtection(context.Background(), TagProtectionConfig{Repository: "eleven-am/golem", Ref: "refs/tags/go/v1.2.3", SHA: sha, Checkout: checkout, Token: "token", APIBaseURL: server.URL, Client: server.Client()})
			if err == nil || err.Error() != "P8_TAG_PROTECTION_REQUIRED" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func tagProtectionRepository(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	runTagGit(t, directory, "init", "-q")
	runTagGit(t, directory, "config", "user.email", "release@example.invalid")
	runTagGit(t, directory, "config", "user.name", "Release Test")
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.test/release\n\ngo 1.23.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTagGit(t, directory, "add", "go.mod")
	runTagGit(t, directory, "commit", "-qm", "candidate")
	runTagGit(t, directory, "tag", "go/v1.2.3")
	sha := strings.TrimSpace(runTagGit(t, directory, "rev-parse", "HEAD"))
	return directory, sha
}

func runTagGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git failed: %v", err)
	}
	return string(output)
}
