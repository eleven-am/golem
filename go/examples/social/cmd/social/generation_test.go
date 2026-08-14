package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestExternalSocialApplicationGenerateCheckBuildAndRun(t *testing.T) {
	exampleRoot := socialHostRoot(t)
	freshRoot := filepath.Join(t.TempDir(), "social")
	copyP8AuthoredApplication(t, exampleRoot, freshRoot)
	freshRoot = canonicalP8Path(t, freshRoot)
	frameworkRoot := goModuleDirectory(t, exampleRoot, "github.com/eleven-am/golem/go")
	frameworkRoot = canonicalP8Path(t, frameworkRoot)
	workspace := filepath.Join(t.TempDir(), "go.work")
	work := fmt.Sprintf("go 1.25.0\nuse (\n\t%s\n\t%s\n)\nreplace github.com/eleven-am/golem/go v0.0.0 => %s\n", filepath.ToSlash(frameworkRoot), filepath.ToSlash(freshRoot), filepath.ToSlash(frameworkRoot))
	if err := os.WriteFile(workspace, []byte(work), 0o600); err != nil {
		t.Fatal(err)
	}
	commandEnv := setP8Environment(os.Environ(), "GOWORK", workspace)
	cli := filepath.Join(t.TempDir(), "golem")
	runP8Command(t, exampleRoot, commandEnv, "go", "build", "-o", cli, "github.com/eleven-am/golem/go/cmd/golem")

	inspect := runP8Command(t, freshRoot, commandEnv, cli, "inspect", "--schema", "./social")
	if !strings.Contains(inspect, `"models"`) || strings.Contains(inspect, `"error"`) {
		t.Fatalf("fresh inspect did not return normalized schema: %s", inspect)
	}
	runP8Command(t, freshRoot, commandEnv, cli, "migration", "new", "--schema", "./social", "--name", "initial", "--migrations", "migrations")
	runP8Command(t, freshRoot, commandEnv, cli, "generate", "--schema", "./social", "--app-out", "./social", "--migrations", "migrations")
	checked := runP8Command(t, freshRoot, commandEnv, cli, "check", "--schema", "./social", "--app-out", "./social", "--migrations", "migrations")
	if !strings.Contains(checked, `"checked": true`) {
		t.Fatalf("fresh generated application was not current: %s", checked)
	}

	databaseDSN := "file:" + filepath.Join(t.TempDir(), "social.sqlite")
	runP8Command(t, freshRoot, commandEnv, cli, "migration", "apply", "--provider", "sqlite", "--dsn", databaseDSN, "--migrations", "migrations")
	binary := filepath.Join(t.TempDir(), "social-server")
	runP8Command(t, freshRoot, commandEnv, "go", "build", "-o", binary, "./cmd/social")
	address := reserveP8Address(t)
	server := exec.Command(binary)
	server.Dir = freshRoot
	server.Env = setP8Environment(commandEnv, "GOLEM_PROVIDER", "sqlite")
	server.Env = setP8Environment(server.Env, "GOLEM_DATABASE_DSN", databaseDSN)
	server.Env = setP8Environment(server.Env, "GOLEM_HTTP_ADDRESS", address)
	var output strings.Builder
	server.Stdout = &output
	server.Stderr = &output
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	var serverExited atomic.Bool
	go func() {
		err := server.Wait()
		serverExited.Store(true)
		stopped <- err
	}()
	t.Cleanup(func() {
		if serverExited.Load() {
			return
		}
		_ = server.Process.Signal(syscall.SIGTERM)
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			_ = server.Process.Kill()
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
			}
		}
	})
	baseURL := "http://" + address
	awaitP8HTTPStatus(t, baseURL+"/health/live", http.StatusNoContent, stopped, &output)
	awaitP8HTTPStatus(t, baseURL+"/health/ready", http.StatusNoContent, stopped, &output)
	request, err := http.NewRequest(http.MethodPost, baseURL+"/graphql", strings.NewReader(`{"query":"query { posts { id } }"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fresh server GraphQL status=%d", response.StatusCode)
	}
	if !bytes.Contains(responseBody, []byte(`"posts":[]`)) || bytes.Contains(responseBody, []byte(`"errors"`)) {
		t.Fatalf("fresh server GraphQL body=%s", responseBody)
	}
	if err := server.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("fresh server shutdown: %v\n%s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		_ = server.Process.Kill()
		t.Fatalf("fresh server did not shut down\n%s", output.String())
	}
}

func canonicalP8Path(t *testing.T, path string) string {
	t.Helper()
	value, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func copyP8AuthoredApplication(t *testing.T, source, destination string) {
	t.Helper()
	allowedRootFiles := map[string]bool{"go.mod": true, "go.sum": true, "tools.go": true, ".gitignore": true}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		slash := filepath.ToSlash(relative)
		if entry.IsDir() {
			if slash == ".golem" || slash == "migrations" || strings.Contains(slash, "/golemgqlgen") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), "_test.go") || strings.HasPrefix(entry.Name(), "zz_golem_") {
			return nil
		}
		if !allowedRootFiles[slash] && !strings.HasPrefix(slash, "social/") && slash != "cmd/social/main.go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func goModuleDirectory(t *testing.T, directory, module string) string {
	t.Helper()
	command := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module)
	command.Dir = directory
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve public module %s: %v\n%s", module, err, output)
	}
	result := strings.TrimSpace(string(output))
	if result == "" || !filepath.IsAbs(result) {
		t.Fatalf("public module directory=%q", result)
	}
	return result
}

func runP8Command(t *testing.T, directory string, environment []string, executable string, arguments ...string) string {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func setP8Environment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func reserveP8Address(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func awaitP8HTTPStatus(t *testing.T, target string, want int, stopped <-chan error, output *strings.Builder) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-stopped:
			t.Fatalf("fresh server stopped before readiness: %v\n%s", err, output.String())
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		response, err := http.DefaultClient.Do(request)
		cancel()
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fresh server endpoint %s did not become ready\n%s", target, output.String())
}
