package golemtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const externalConsumerModule = `module example.com/golem-policy-kit-consumer

go 1.25.0

require (
	github.com/eleven-am/golem/go v0.0.0
	github.com/eleven-am/golem/go/examples/social v0.0.0
)
`

const externalConsumerSource = `package consumer

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/golemtest"
	"github.com/eleven-am/golem/go/examples/social/social"
)

func TestExternalApplicationBuildsActorPolicyWithoutDatabase(t *testing.T) {
	bindings, err := social.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := social.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}

	kit, err := golemtest.New(bindings, descriptors, social.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	author := golem.UUID{0x01}
	policies, err := kit.ForActor(social.Actor{UserID: author, Authenticated: true})
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}

	posts, err := golemtest.Model(policies, social.GolemGeneratedPostDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	read, err := posts.RowConstraint(golem.FrozenActionRead)
	if err != nil {
		t.Fatalf("RowConstraint: %v", err)
	}
	if _, constant := read.Constant(); constant {
		t.Fatal("the authored read policy collapsed to a constant")
	}
	if len(read.CanonicalBytes()) == 0 {
		t.Fatal("the resolved constraint carries no canonical evidence")
	}

	expected := social.Posts.Published.Eq(true).Or(social.Posts.AuthorID.Eq(author))
	equivalent, err := golemtest.Equivalent(read, expected)
	if err != nil || !equivalent {
		t.Fatalf("read constraint equivalent=%v err=%v", equivalent, err)
	}

	wider := expected.Or(social.Posts.Title.Eq("anything"))
	implied, err := golemtest.Implies(read, wider)
	if err != nil || !implied {
		t.Fatalf("read constraint implies wider=%v err=%v", implied, err)
	}
	narrower := social.Posts.AuthorID.Eq(author)
	implied, err = golemtest.Implies(read, narrower)
	if err != nil {
		t.Fatalf("Implies narrower: %v", err)
	}
	if implied {
		t.Fatal("the read constraint was reported to imply a strictly narrower predicate")
	}

	anonymous, err := kit.ForActor(social.Actor{})
	if err != nil {
		t.Fatalf("ForActor anonymous: %v", err)
	}
	if _, err := golemtest.Model(anonymous, social.GolemGeneratedPostDescriptor); err != nil {
		t.Fatalf("Model anonymous: %v", err)
	}

	foreign, err := golemtest.Model(policies, social.GolemGeneratedUserDescriptor)
	if err != nil {
		t.Fatalf("Model user: %v", err)
	}
	_ = foreign

	if _, ok := golemtest.CodeOf(err); ok {
		t.Fatal("nil error must carry no classification")
	}
}
`

func TestPolicyTestKitExternalConsumerCompilesAndRunsWithoutWorkspace(t *testing.T) {
	if testing.Short() {
		t.Skip("external module build")
	}

	moduleRoot := filepath.Dir(packageDirectory(t))
	exampleRoot := filepath.Join(moduleRoot, "examples", "social")

	if strings.Contains(externalConsumerSource, "/internal/") {
		t.Fatal("external consumer must depend on public packages only")
	}
	if strings.Contains(externalConsumerModule, "replace") {
		t.Fatal("external consumer module text must declare its replacements explicitly below")
	}

	consumer := filepath.Join(t.TempDir(), "consumer")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		t.Fatal(err)
	}
	consumer, err := filepath.EvalSymlinks(consumer)
	if err != nil {
		t.Fatal(err)
	}

	manifest := externalConsumerModule +
		"\nreplace github.com/eleven-am/golem/go => " + moduleRoot +
		"\n\nreplace github.com/eleven-am/golem/go/examples/social => " + exampleRoot + "\n"
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "consumer_test.go"), []byte(externalConsumerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(moduleRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	exampleSum, err := os.ReadFile(filepath.Join(exampleRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "go.sum"), append(append([]byte(nil), sum...), exampleSum...), 0o644); err != nil {
		t.Fatal(err)
	}

	environment := append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = consumer
	tidy.Env = environment
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v\n%s", err, output)
	}

	verify := exec.Command("go", "test", "-race", "./...")
	verify.Dir = consumer
	verify.Env = environment
	output, err := verify.CombinedOutput()
	if err != nil {
		t.Fatalf("external consumer test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "ok") {
		t.Fatalf("external consumer produced no passing package: %s", output)
	}

	written, err := os.ReadFile(filepath.Join(consumer, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(written), "replace") != 2 {
		t.Fatalf("external consumer resolved through unexpected replacements:\n%s", written)
	}

	_ = runtime.NumGoroutine()
}
