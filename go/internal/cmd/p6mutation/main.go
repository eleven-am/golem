package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/mutationverify"
)

func main() {
	module := flag.String("module", ".", "path to the Go module")
	labels := flag.String("labels", "", "comma-separated labels (default: every catalog mutation)")
	list := flag.Bool("list", false, "list complete covered/remaining inventory")
	allowUncovered := flag.Bool("allow-uncovered", false, "development only: omit catalog entries that have no executable mutant")
	keep := flag.Bool("keep", false, "keep isolated mutant module copies")
	timeout := flag.Duration("timeout", 10*time.Minute, "timeout per named test")
	flag.Parse()
	catalog := mutationverify.Catalog()
	if *list {
		for _, mutation := range catalog {
			state := "COVERED"
			detail := mutation.Summary
			if !mutation.Covered() {
				state, detail = "REMAINING", mutation.Remaining
			}
			fmt.Printf("%-32s %-9s %s\n", mutation.Label, state, detail)
		}
		return
	}
	selected, err := selectMutations(catalog, *labels, *allowUncovered)
	if err != nil {
		fatal(err)
	}
	root, err := filepath.Abs(*module)
	if err != nil {
		fatal(err)
	}
	runner := mutationverify.Runner{ModuleDir: root, Keep: *keep, Timeout: *timeout}
	failed := false
	for _, mutation := range selected {
		result, runErr := runner.Run(context.Background(), mutation)
		if runErr != nil {
			fatal(runErr)
		}
		fmt.Printf("%-32s %-8s %s", result.Label, result.Status, result.Duration.Round(time.Millisecond))
		if result.Test != "" {
			fmt.Printf(" %s", result.Test)
		}
		if result.Detail != "" {
			fmt.Printf(" (%s)", result.Detail)
		}
		if result.SandboxDir != "" {
			fmt.Printf(" [%s]", result.SandboxDir)
		}
		fmt.Println()
		if result.Status != mutationverify.StatusKilled {
			failed = true
			if result.Output != "" {
				fmt.Fprintln(os.Stderr, result.Output)
			}
		}
	}
	if failed {
		os.Exit(1)
	}
}

func selectMutations(catalog []mutationverify.Mutation, labels string, allowUncovered bool) ([]mutationverify.Mutation, error) {
	if strings.TrimSpace(labels) == "" {
		if !allowUncovered {
			return append([]mutationverify.Mutation(nil), catalog...), nil
		}
		result := make([]mutationverify.Mutation, 0, len(catalog))
		for _, mutation := range catalog {
			if mutation.Covered() {
				result = append(result, mutation)
			}
		}
		return result, nil
	}
	wanted := map[string]bool{}
	for _, label := range strings.Split(labels, ",") {
		label = strings.TrimSpace(label)
		if label != "" {
			wanted[label] = true
		}
	}
	result := []mutationverify.Mutation{}
	for _, mutation := range catalog {
		if wanted[mutation.Label] {
			result = append(result, mutation)
			delete(wanted, mutation.Label)
		}
	}
	if len(wanted) != 0 {
		unknown := make([]string, 0, len(wanted))
		for label := range wanted {
			unknown = append(unknown, label)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown mutation labels: %s", strings.Join(unknown, ", "))
	}
	return result, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
