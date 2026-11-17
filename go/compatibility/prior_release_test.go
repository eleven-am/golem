package compatibility

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func preparePriorRelease(t *testing.T) string {
	t.Helper()
	// Copy only this small pinned module; offline normalization must never
	// change the checked-in fixture. Production release source is not copied.
	dir := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum", "codec_test.go", "fixture_test.go", "queue_test.go", "backup_test.go"} {
		data, err := os.ReadFile(filepath.Join("testdata", "priorrelease", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runPriorRelease(t *testing.T, dir, testName string) {
	t.Helper()
	runOfflineConsumerGo(t, dir, "test", "-mod=mod", "-p=1", "-count=1", "-timeout=30s", "-run=^"+testName+"$", ".")
}

// runOfflineConsumerGo is the shared bounded, serial source-consumer command
// owner. All module normalization happens in the disposable consumer directory.
func runOfflineConsumerGo(t *testing.T, dir string, arguments ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "nice", append([]string{"-n", "19", "go"}, arguments...)...)
	command.Dir = dir
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOWORK", "GOFLAGS", "GOMAXPROCS", "GOLEM_COMPAT_FIXTURES", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOVCS", "GOMEMLIMIT":
			continue
		}
		command.Env = append(command.Env, value)
	}
	command.Env = append(command.Env, "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "GOFLAGS=-p=1", "GOMAXPROCS=1", "GOMEMLIMIT=256MiB", "GOPRIVATE=", "GONOPROXY=none", "GONOSUMDB=none", "GOVCS=*:off", "GOLEM_COMPAT_FIXTURES="+dir)
	var output limitedOutput
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		t.Fatalf("offline consumer go %v: %v\n%s", arguments, err, output.String())
	}
	return output.String()
}

type limitedOutput struct{ bytes.Buffer }

func (output *limitedOutput) Write(value []byte) (int, error) {
	length := len(value)
	if remaining := (32 << 10) - output.Len(); remaining > 0 {
		_, _ = output.Buffer.Write(value[:min(len(value), remaining)])
	}
	return length, nil
}
