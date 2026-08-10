package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/eleven-am/golem/go/internal/workflowaudit"
)

type pathsFlag []string

func (values *pathsFlag) String() string { return fmt.Sprint([]string(*values)) }
func (values *pathsFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	var events pathsFlag
	var requiredTests pathsFlag
	var allowedSkips pathsFlag
	profile := flag.String("profile", "", "closed hosted evidence profile")
	output := flag.String("output", "", "structured evidence output path")
	inventory := flag.String("inventory", "", "checked required-test inventory")
	inventorySet := flag.String("inventory-set", "", "closed inventory set name")
	rejectSkips := flag.Bool("reject-skips", false, "fail on every skipped test or package")
	verifyTagProtection := flag.Bool("verify-tag-protection", false, "verify the checked candidate tag is protected and immutable")
	repository := flag.String("repository", "", "GitHub owner/repository for tag protection verification")
	ref := flag.String("ref", "", "exact refs/tags/go/vX.Y.Z ref")
	sha := flag.String("sha", "", "exact checked out candidate commit")
	checkout := flag.String("checkout", ".", "candidate repository checkout")
	flag.Var(&events, "events", "go test -json event file; repeat for multiple commands")
	flag.Var(&requiredTests, "require-test", "exact package:test identity that must pass; repeatable")
	flag.Var(&allowedSkips, "allow-skip", "exact non-required skip identity; repeatable")
	flag.Parse()
	if *verifyTagProtection {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		evidence, err := workflowaudit.VerifyTagProtection(ctx, workflowaudit.TagProtectionConfig{Repository: *repository, Ref: *ref, SHA: *sha, Checkout: *checkout, Token: os.Getenv("GITHUB_TOKEN")})
		writeOutput(*output, evidence)
		if err != nil {
			fatal(err.Error())
		}
		return
	}
	if *inventory != "" || *inventorySet != "" {
		set, err := workflowaudit.ReadRequiredTestSet(*inventory, *inventorySet)
		if err != nil {
			fatal(err.Error())
		}
		requiredTests = append(requiredTests, set.Required...)
		allowedSkips = append(allowedSkips, set.AllowedSkips...)
	}
	audit, err := workflowaudit.AuditTestEvents(*profile, events, requiredTests, allowedSkips, *rejectSkips)
	writeOutput(*output, audit)
	if err != nil {
		fatal(err.Error())
	}
}

func writeOutput(path string, value any) {
	encoded, encodeErr := json.MarshalIndent(value, "", "  ")
	if encodeErr != nil {
		fatal("P8_WORKFLOW_AUDIT_ENCODE")
	}
	encoded = append(encoded, '\n')
	if path == "" {
		_, _ = os.Stdout.Write(encoded)
	} else if writeErr := os.WriteFile(path, encoded, 0o600); writeErr != nil {
		fatal("P8_WORKFLOW_AUDIT_WRITE")
	}
}

func fatal(code string) {
	fmt.Fprintln(os.Stderr, code)
	os.Exit(1)
}
