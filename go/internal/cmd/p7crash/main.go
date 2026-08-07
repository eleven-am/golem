package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/eleven-am/golem/go/internal/p7verify"
)

func main() {
	module := flag.String("module", ".", "path to the Go module")
	timeout := flag.Duration("timeout", 20*time.Minute, "overall crash matrix deadline")
	child := flag.Bool("child", false, "private crash worker mode")
	provider := flag.String("provider", "", "private child provider")
	mode := flag.String("mode", "", "private child boundary")
	causation := flag.String("causation", "", "private child causation")
	database := flag.String("db", "", "private child SQLite file")
	namespace := flag.String("namespace", "", "private child PostgreSQL namespace")
	ready := flag.String("ready", "", "private child crash signal")
	acceptance := flag.String("acceptance", "", "private child acceptance journal")
	result := flag.String("result", "", "private child result path")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *child {
		err := p7verify.RunCrashChild(ctx, p7verify.CrashChildConfig{
			Provider: *provider, DBPath: *database, Namespace: *namespace, Mode: *mode,
			Causation: *causation, ReadyPath: *ready, LogPath: *acceptance, Result: *result,
		})
		if err != nil {
			fatal(err)
		}
		return
	}
	root, err := filepath.Abs(*module)
	if err != nil {
		fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "go.mod")); err != nil || info.IsDir() {
		fatal(fmt.Errorf("-module must identify the Go module containing go.mod"))
	}
	encoder := json.NewEncoder(os.Stdout)
	err = p7verify.RunCrashMatrix(ctx, p7verify.CrashConfig{Writer: func(evidence p7verify.CrashEvidence) {
		_ = encoder.Encode(evidence)
	}})
	if err != nil {
		fatal(err)
	}
	_ = encoder.Encode(map[string]any{"command": "p7crash", "profiles": 3, "boundaries": 5, "status": "PASS"})
}

func fatal(err error) {
	encoded, _ := json.Marshal(map[string]any{"command": "p7crash", "status": "FAIL", "error": err.Error()})
	fmt.Fprintln(os.Stderr, string(encoded))
	os.Exit(1)
}
