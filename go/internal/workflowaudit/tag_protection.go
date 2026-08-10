package workflowaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
)

const tagProtectionFormatVersion = 1

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	releaseRefPattern = regexp.MustCompile(`^refs/tags/go/v[0-9]+\.[0-9]+\.[0-9]+$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type TagProtectionConfig struct {
	Repository string
	Ref        string
	SHA        string
	Checkout   string
	Token      string
	APIBaseURL string
	Client     *http.Client
}

type TagProtectionEvidence struct {
	FormatVersion int    `json:"formatVersion"`
	Repository    string `json:"repository"`
	Ref           string `json:"ref"`
	SHA           string `json:"sha"`
	RulesetID     int64  `json:"rulesetId"`
	Status        string `json:"status"`
}

type rulesetSummary struct {
	ID          int64  `json:"id"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
}

type rulesetDetail struct {
	ID          int64  `json:"id"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
	Conditions  struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []struct {
		Type string `json:"type"`
	} `json:"rules"`
}

// VerifyTagProtection proves that the checked-out tag equals GITHUB_SHA and is
// covered by an active immutable signed-tag ruleset. It returns only closed
// evidence; API bodies, tokens, rule names, and transport errors never escape.
func VerifyTagProtection(ctx context.Context, config TagProtectionConfig) (TagProtectionEvidence, error) {
	evidence := TagProtectionEvidence{FormatVersion: tagProtectionFormatVersion, Repository: config.Repository, Ref: config.Ref, SHA: config.SHA, Status: "FAIL"}
	if ctx == nil || !repositoryPattern.MatchString(config.Repository) || !releaseRefPattern.MatchString(config.Ref) || !commitPattern.MatchString(config.SHA) || config.Checkout == "" || config.Token == "" {
		return evidence, errors.New("P8_TAG_PROTECTION_CONFIG")
	}
	gitContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(gitContext, "git", "rev-parse", "--verify", config.Ref+"^{commit}")
	command.Dir = config.Checkout
	resolved, err := command.Output()
	if err != nil || strings.TrimSpace(string(resolved)) != config.SHA {
		return evidence, errors.New("P8_TAG_PROTECTION_SHA_MISMATCH")
	}
	base := strings.TrimSuffix(config.APIBaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	var summaries []rulesetSummary
	endpoint := fmt.Sprintf("%s/repos/%s/rulesets?includes_parents=true&targets=tag&per_page=100", base, config.Repository)
	if err := getGitHubJSON(ctx, client, endpoint, config.Token, &summaries); err != nil || len(summaries) == 100 {
		return evidence, errors.New("P8_TAG_PROTECTION_QUERY")
	}
	for _, summary := range summaries {
		if summary.ID <= 0 || summary.Target != "tag" || summary.Enforcement != "active" {
			continue
		}
		var detail rulesetDetail
		endpoint = fmt.Sprintf("%s/repos/%s/rulesets/%d?includes_parents=true", base, config.Repository, summary.ID)
		if err := getGitHubJSON(ctx, client, endpoint, config.Token, &detail); err != nil {
			return evidence, errors.New("P8_TAG_PROTECTION_QUERY")
		}
		if detail.ID != summary.ID || detail.Target != "tag" || detail.Enforcement != "active" || !rulesetMatchesRef(detail, config.Ref) || !rulesetIsImmutableAndSigned(detail) {
			continue
		}
		evidence.RulesetID = detail.ID
		evidence.Status = "PASS"
		return evidence, nil
	}
	return evidence, errors.New("P8_TAG_PROTECTION_REQUIRED")
}

func getGitHubJSON(ctx context.Context, client *http.Client, endpoint, token string, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("unexpected status")
	}
	return json.NewDecoder(response.Body).Decode(destination)
}

func rulesetMatchesRef(detail rulesetDetail, ref string) bool {
	included := false
	for _, pattern := range detail.Conditions.RefName.Include {
		if refPatternMatches(pattern, ref) {
			included = true
			break
		}
	}
	if !included {
		return false
	}
	for _, pattern := range detail.Conditions.RefName.Exclude {
		if refPatternMatches(pattern, ref) {
			return false
		}
	}
	return true
}

func refPatternMatches(pattern, ref string) bool {
	if pattern == "~ALL" {
		return true
	}
	matched, err := path.Match(pattern, ref)
	return err == nil && matched
}

func rulesetIsImmutableAndSigned(detail rulesetDetail) bool {
	required := map[string]bool{"deletion": false, "non_fast_forward": false, "required_signatures": false}
	for _, rule := range detail.Rules {
		if _, ok := required[rule.Type]; ok {
			required[rule.Type] = true
		}
	}
	return required["deletion"] && required["non_fast_forward"] && required["required_signatures"]
}
