package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

func main() {
	repository := flag.String("repository", "..", "repository root")
	module := flag.String("module", "", "Go module path; its parent is used as repository root")
	labels := flag.String("labels", "", "comma-separated mutation labels")
	timeout := flag.Duration("timeout", 15*time.Minute, "maximum timeout per mutation")
	list := flag.Bool("list", false, "emit the closed mutation inventory")
	flag.Parse()
	if *module != "" {
		absolute, err := filepath.Abs(*module)
		if err != nil {
			fatal("P8_MUTATION_MODULE_INVALID")
		}
		*repository = filepath.Dir(absolute)
	}
	catalog := p8mutation.Catalog()
	if err := p8mutation.ValidateCatalog(catalog); err != nil {
		fatal(err.Error())
	}
	selected, err := selectMutations(catalog, *labels)
	if err != nil {
		fatal(err.Error())
	}
	encoder := json.NewEncoder(os.Stdout)
	if *list {
		for _, mutation := range selected {
			_ = encoder.Encode(map[string]any{"formatVersion": 1, "mutation": mutation.Label, "status": "EXECUTABLE", "test": mutation.Gate.Test, "timeout": mutation.Timeout.String()})
		}
		return
	}
	runner := p8mutation.Runner{Repository: *repository, Timeout: *timeout}
	failed := false
	for _, mutation := range selected {
		result, runErr := runner.Run(context.Background(), mutation)
		if runErr != nil {
			fatal("P8_MUTATION_RUNNER_FAILED")
		}
		_ = encoder.Encode(result)
		if result.Status != p8mutation.StatusKilled {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
	_ = encoder.Encode(map[string]any{"formatVersion": 1, "command": "p8mutation", "mutations": len(selected), "status": "PASS"})
}

func selectMutations(catalog []p8mutation.Mutation, labels string) ([]p8mutation.Mutation, error) {
	if strings.TrimSpace(labels) == "" {
		return catalog, nil
	}
	wanted := map[string]bool{}
	for _, label := range strings.Split(labels, ",") {
		if label = strings.TrimSpace(label); label != "" {
			wanted[label] = true
		}
	}
	selected := []p8mutation.Mutation{}
	for _, mutation := range catalog {
		if wanted[mutation.Label] {
			selected = append(selected, mutation)
			delete(wanted, mutation.Label)
		}
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("P8_MUTATION_UNKNOWN_LABEL")
	}
	return selected, nil
}

func fatal(code string) {
	encoded, _ := json.Marshal(map[string]any{"formatVersion": 1, "command": "p8mutation", "status": "FAIL", "error": code})
	fmt.Fprintln(os.Stderr, string(encoded))
	os.Exit(2)
}
