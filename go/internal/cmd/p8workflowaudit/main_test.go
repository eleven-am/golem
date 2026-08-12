package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestP8WorkflowAuditFlagsAndOutputOwnExactValues(t *testing.T) {
	var paths pathsFlag
	if err := paths.Set("first.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := paths.Set("second.jsonl"); err != nil || paths.String() != "[first.jsonl second.jsonl]" {
		t.Fatalf("paths=%s err=%v", paths.String(), err)
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	writeOutput(path, map[string]any{"formatVersion": 1, "status": "PASS"})
	encoded, err := os.ReadFile(path)
	if err != nil || string(encoded) != "{\n  \"formatVersion\": 1,\n  \"status\": \"PASS\"\n}\n" {
		t.Fatalf("output=%q err=%v", encoded, err)
	}
}
