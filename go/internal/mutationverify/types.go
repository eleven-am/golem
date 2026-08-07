// Package mutationverify runs P6's named mutations against isolated copies of
// the Go module. It never edits the caller's working tree.
package mutationverify

import "time"

type Patch struct {
	Path   string
	Before string
	After  string
}

type Test struct {
	Package string
	Name    string
	Env     []string
}

type Mutation struct {
	Label     string
	Summary   string
	Patches   []Patch
	Tests     []Test
	Remaining string
}

func (mutation Mutation) Covered() bool {
	return len(mutation.Patches) != 0 && len(mutation.Tests) != 0
}

type Status string

const (
	StatusKilled   Status = "KILLED"
	StatusSurvived Status = "SURVIVED"
	StatusInvalid  Status = "INVALID"
	StatusSkipped  Status = "SKIPPED"
)

type Result struct {
	Label      string
	Status     Status
	Test       string
	Duration   time.Duration
	Output     string
	Detail     string
	SandboxDir string
}
