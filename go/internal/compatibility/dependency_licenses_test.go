package compatibility

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestDependencyLicenseAuthorityIsCanonicalDigestBoundAndDetached(t *testing.T) {
	root := dependencyLicenseModuleRoot(t)
	encoded := dependencyLicenseRead(t, root, DependencyLicenseAuthorityPath)
	if got := DependencyLicenseAuthorityDigest(encoded); got != "f5c8341f5d50f78d9f3b8dc2ae15adb9d9866a1f1cd3495ef83ef354525b0e65" {
		t.Fatalf("dependency license authority digest=%s", got)
	}
	if DependencyLicenseAuthoritySHA256 != "f5c8341f5d50f78d9f3b8dc2ae15adb9d9866a1f1cd3495ef83ef354525b0e65" {
		t.Fatalf("dependency license source digest=%s", DependencyLicenseAuthoritySHA256)
	}
	value, err := ParseDependencyLicenseAuthority(encoded, DependencyLicenseAuthoritySHA256)
	if err != nil {
		t.Fatal(err)
	}
	if value.Project.Path != "LICENSE" || value.Project.LicenseDeclared != "LicenseRef-Golem-GPLv3-Unspecified" || value.Project.SHA256 != "3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986" || value.Notices.Path != "THIRD_PARTY_NOTICES" || value.Notices.SHA256 != "9fcb303bac3add18c77bbae96712323cb9bf094505e2dc0092e3b58e4482cd41" {
		t.Fatalf("project/notices authority=%+v %+v", value.Project, value.Notices)
	}
	want := []DependencyLicense{
		{Module: "github.com/klauspost/compress", Version: "v1.18.5", LicenseDeclared: "LicenseRef-Klauspost-Compress-Composite", LicenseSHA256: "0d9e582ee4bff57bf1189c9e514e6da7ce277f9cd3bc2d488b22fbb39a6d87cf"},
		{Module: "github.com/nats-io/nats.go", Version: "v1.52.0", LicenseDeclared: "Apache-2.0", LicenseSHA256: "c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4"},
		{Module: "github.com/nats-io/nkeys", Version: "v0.4.15", LicenseDeclared: "Apache-2.0", LicenseSHA256: "c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4"},
		{Module: "github.com/nats-io/nuid", Version: "v1.0.1", LicenseDeclared: "Apache-2.0", LicenseSHA256: "c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4"},
		{Module: "golang.org/x/crypto", Version: "v0.49.0", LicenseDeclared: "BSD-3-Clause", LicenseSHA256: "911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad"},
		{Module: "golang.org/x/sys", Version: "v0.47.0", LicenseDeclared: "BSD-3-Clause", LicenseSHA256: "911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad"},
	}
	if len(value.Dependencies) != len(want) {
		t.Fatalf("dependency inventory=%+v", value.Dependencies)
	}
	for index := range want {
		if value.Dependencies[index] != want[index] {
			t.Fatalf("dependency[%d]=%+v want %+v", index, value.Dependencies[index], want[index])
		}
	}
	moduleFile, err := modfile.Parse("go.mod", dependencyLicenseRead(t, root, "go.mod"), nil)
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]string{}
	for _, requirement := range moduleFile.Require {
		selected[requirement.Mod.Path] = requirement.Mod.Version
	}
	for _, dependency := range want {
		if selected[dependency.Module] != dependency.Version {
			t.Fatalf("selected dependency %s=%q want %q", dependency.Module, selected[dependency.Module], dependency.Version)
		}
	}
	if err := ValidateDependencyLicenseFiles(value, dependencyLicenseRead(t, root, ProjectLicensePath), dependencyLicenseRead(t, root, ThirdPartyNoticesPath)); err != nil {
		t.Fatal(err)
	}
	value.Dependencies[0].Version = "v9.9.9"
	reparsed, err := ParseDependencyLicenseAuthority(encoded, DependencyLicenseAuthoritySHA256)
	if err != nil || reparsed.Dependencies[0] != want[0] {
		t.Fatalf("parsed authority aliased mutable result: %+v %v", reparsed.Dependencies[0], err)
	}
}

func TestDependencyLicenseAuthorityRejectsHostileEvidence(t *testing.T) {
	root := dependencyLicenseModuleRoot(t)
	encoded := dependencyLicenseRead(t, root, DependencyLicenseAuthorityPath)
	authority, err := ParseDependencyLicenseAuthority(encoded, DependencyLicenseAuthoritySHA256)
	if err != nil {
		t.Fatal(err)
	}
	license := dependencyLicenseRead(t, root, ProjectLicensePath)
	notices := dependencyLicenseRead(t, root, ThirdPartyNoticesPath)
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{name: "wrong-authority-digest", run: func() error {
			_, err := ParseDependencyLicenseAuthority(encoded, string(bytes.Repeat([]byte{'0'}, 64)))
			return err
		}},
		{name: "unknown-field", run: func() error {
			_, err := ParseDependencyLicenseAuthority(bytes.Replace(encoded, []byte("  \"formatVersion\":"), []byte("  \"unknown\": 1,\n  \"formatVersion\":"), 1), DependencyLicenseAuthoritySHA256)
			return err
		}},
		{name: "noncanonical", run: func() error {
			hostile := append([]byte(" \n"), encoded...)
			_, err := ParseDependencyLicenseAuthority(hostile, DependencyLicenseAuthorityDigest(hostile))
			return err
		}},
		{name: "tampered-project-license", run: func() error {
			return ValidateDependencyLicenseFiles(authority, append(append([]byte(nil), license...), 'x'), notices)
		}},
		{name: "tampered-notice-license", run: func() error {
			hostile := bytes.Replace(notices, []byte("Apache License"), []byte("Apache license"), 1)
			hostileAuthority := authority
			hostileAuthority.Notices.SHA256 = digest(hostile)
			return ValidateDependencyLicenseFiles(hostileAuthority, license, hostile)
		}},
		{name: "tampered-notice-header", run: func() error {
			hostile := bytes.Replace(notices, []byte("This distribution includes"), []byte("This distribution omits"), 1)
			return ValidateDependencyLicenseFiles(authority, license, hostile)
		}},
		{name: "truncated-composite-license", run: func() error {
			hostile := bytes.Replace(notices, []byte("Files: gzhttp/*\n"), nil, 1)
			hostileAuthority := authority
			hostileAuthority.Notices.SHA256 = digest(hostile)
			return ValidateDependencyLicenseFiles(hostileAuthority, license, hostile)
		}},
		{name: "surplus-section", run: func() error {
			hostile := append(append([]byte(nil), notices...), []byte("----- BEGIN LICENSE example.test/surplus@v1.0.0 -----\nx\n----- END LICENSE example.test/surplus@v1.0.0 -----\n")...)
			return ValidateDependencyLicenseFiles(authority, license, hostile)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil {
				t.Fatal("hostile dependency license evidence accepted")
			}
		})
	}
}

func TestDependencyLicenseAuthorityExactlyCoversNATSPackageModuleClosure(t *testing.T) {
	root := dependencyLicenseModuleRoot(t)
	authority, err := ParseDependencyLicenseAuthority(dependencyLicenseRead(t, root, DependencyLicenseAuthorityPath), DependencyLicenseAuthoritySHA256)
	if err != nil {
		t.Fatal(err)
	}
	direct := exec.Command("go", "list", "-json", "./events/nats")
	direct.Dir = root
	direct.Env = append(os.Environ(), "GOWORK=off")
	directBytes, err := direct.Output()
	if err != nil {
		t.Fatal(err)
	}
	var packageIdentity struct{ Imports []string }
	if err := json.Unmarshal(directBytes, &packageIdentity); err != nil {
		t.Fatal(err)
	}
	wantImports := []string{
		"context", "encoding/hex", "github.com/eleven-am/golem/go/events", "github.com/eleven-am/golem/go/golem", "github.com/eleven-am/golem/go/provider", "github.com/nats-io/nats.go", "net", "regexp", "strings", "sync", "sync/atomic", "time",
	}
	if !slices.Equal(packageIdentity.Imports, wantImports) {
		t.Fatalf("events/nats direct imports=%v want exact reviewed boundary %v", packageIdentity.Imports, wantImports)
	}
	command := exec.Command("go", "mod", "graph")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	encoded, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	moduleFile, err := modfile.Parse("go.mod", dependencyLicenseRead(t, root, "go.mod"), nil)
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]string{}
	for _, requirement := range moduleFile.Require {
		selected[requirement.Mod.Path] = requirement.Mod.Version
	}
	actual := map[string]string{"github.com/nats-io/nats.go": selected["github.com/nats-io/nats.go"]}
	for _, line := range bytes.Split(bytes.TrimSpace(encoded), []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) != 2 || string(fields[0]) != "github.com/nats-io/nats.go@v1.52.0" || bytes.HasPrefix(fields[1], []byte("go@")) {
			continue
		}
		module, _, ok := strings.Cut(string(fields[1]), "@")
		if !ok || selected[module] == "" {
			t.Fatalf("NATS module graph edge lacks selected root version: %s", line)
		}
		actual[module] = selected[module]
	}
	want := map[string]string{}
	for _, dependency := range authority.Dependencies {
		want[dependency.Module] = dependency.Version
	}
	if len(actual) != len(want) {
		t.Fatalf("events/nats external module closure=%v want exact governed inventory %v", actual, want)
	}
	for module, version := range want {
		if actual[module] != version {
			t.Fatalf("events/nats module %s=%q want %q", module, actual[module], version)
		}
	}
	if declared, ok := authority.LicenseFor("github.com/nats-io/nats.go", "v1.52.1"); ok || declared != "" {
		t.Fatalf("LicenseFor accepted wrong selected version: %q/%t", declared, ok)
	}
}

func dependencyLicenseModuleRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func dependencyLicenseRead(t *testing.T, root, relative string) []byte {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
