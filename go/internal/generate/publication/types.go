// Package publication validates, checks, and crash-recoverably publishes a
// complete manifest-owned generated artifact set.
package publication

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
)

type Mode string
type Step string

const (
	ModePublish Mode = "publish"
	ModeCheck   Mode = "check"

	StepStaged            Step = "staged"
	StepJournaled         Step = "journaled"
	StepBackedUp          Step = "backed_up"
	StepInstalled         Step = "installed"
	StepVerified          Step = "verified"
	StepManifestInstalled Step = "manifest_installed"
)

type FailureInjector func(step Step, moduleRelativePath string) error

type Request struct {
	ModuleDir    string
	ManifestPath string
	Prospective  manifest.Result
	Mode         Mode
	FileMode     uint32
	Inject       FailureInjector
}

type Result struct {
	Changed []string
	Stale   []string
	Checked bool
}

func Apply(ctx context.Context, request Request) (Result, error) { return apply(ctx, request) }

// Recover acquires the generation lock and resolves any interrupted publication
// journal. The journal's validated ManifestPath is authoritative: callers do
// not need to know which generated-code or migration-history publication was
// interrupted. The optional legacy argument is ignored so older internal
// callers remain source-compatible while migrating to the routed API.
func Recover(ctx context.Context, moduleDir string, legacyManifestPath ...string) error {
	if len(legacyManifestPath) > 1 {
		return fmt.Errorf("publication recovery accepts at most one legacy manifest path")
	}
	return recoverOnly(ctx, moduleDir)
}
