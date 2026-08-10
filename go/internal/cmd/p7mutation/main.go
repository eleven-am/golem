package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/mutationverify"
	"github.com/eleven-am/golem/go/internal/p7verify"
)

type mutationEvidence struct {
	Mutation string `json:"mutation"`
	Status   string `json:"status"`
	Test     string `json:"test,omitempty"`
	Duration string `json:"duration"`
	Detail   string `json:"detail,omitempty"`
	Output   string `json:"output,omitempty"`
}

func main() {
	module := flag.String("module", ".", "path to the Go module")
	labels := flag.String("labels", "", "comma-separated mutation labels (default: the complete P7 catalog)")
	list := flag.Bool("list", false, "emit the complete executable inventory")
	keep := flag.Bool("keep", false, "keep isolated mutant module copies")
	timeout := flag.Duration("timeout", 10*time.Minute, "timeout per named killing test")
	flag.Parse()
	catalog := p7verify.MutationCatalog()
	encoder := json.NewEncoder(os.Stdout)
	if *list {
		for _, mutation := range catalog {
			_ = encoder.Encode(map[string]any{"mutation": mutation.Label, "status": "COVERED", "patches": len(mutation.Patches), "tests": len(mutation.Tests)})
		}
		return
	}
	selected, err := selectMutations(catalog, *labels)
	if err != nil {
		fatal(err)
	}
	root, err := filepath.Abs(*module)
	if err != nil {
		fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "go.mod")); err != nil || info.IsDir() {
		fatal(fmt.Errorf("-module must identify the Go module containing go.mod"))
	}
	runner := mutationverify.Runner{ModuleDir: root, Keep: *keep, Timeout: *timeout}
	failed := false
	for _, mutation := range selected {
		result, runErr := runner.Run(context.Background(), mutation)
		if runErr != nil {
			fatal(runErr)
		}
		record := mutationEvidence{Mutation: result.Label, Status: string(result.Status), Test: result.Test, Duration: result.Duration.Round(time.Millisecond).String(), Detail: result.Detail}
		if result.Status != mutationverify.StatusKilled {
			failed = true
			record.Output = bounded(result.Output, 8192)
		}
		_ = encoder.Encode(record)
	}
	if failed {
		os.Exit(1)
	}
	_ = encoder.Encode(map[string]any{"command": "p7mutation", "mutations": len(selected), "status": "PASS"})
}

func selectMutations(catalog []mutationverify.Mutation, labels string) ([]mutationverify.Mutation, error) {
	if strings.TrimSpace(labels) == "" {
		return append([]mutationverify.Mutation(nil), catalog...), nil
	}
	wanted := map[string]bool{}
	for _, label := range strings.Split(labels, ",") {
		if label = strings.TrimSpace(label); label != "" {
			wanted[label] = true
		}
	}
	selected := []mutationverify.Mutation{}
	for _, mutation := range catalog {
		if wanted[mutation.Label] {
			selected = append(selected, mutation)
			delete(wanted, mutation.Label)
		}
	}
	if len(wanted) != 0 {
		unknown := make([]string, 0, len(wanted))
		for label := range wanted {
			unknown = append(unknown, label)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown P7 mutations: %s", strings.Join(unknown, ","))
	}
	return selected, nil
}

func bounded(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum] + "..."
}

func fatal(err error) {
	encoded, _ := json.Marshal(map[string]any{"command": "p7mutation", "status": "FAIL", "error": err.Error()})
	fmt.Fprintln(os.Stderr, string(encoded))
	os.Exit(2)
}
