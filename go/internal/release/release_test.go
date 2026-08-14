package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compatibility"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func TestReleaseTagAndVersionAgreement(t *testing.T) {
	commit := strings.Repeat("a", 40)
	manifest := compatibility.DevelopmentManifest()
	manifest.Release = compatibility.Release{Version: "v1.2.3", Tag: "go/v1.2.3", Commit: commit}
	if err := ValidateAgreement(ModulePath, "go/v1.2.3", commit, manifest); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, module, tag, commit string
		manifest                  compatibility.Manifest
	}{
		{name: "unprefixed nested tag", module: ModulePath, tag: "v1.2.3", commit: commit, manifest: manifest},
		{name: "wrong nested prefix", module: ModulePath, tag: "javascript/v1.2.3", commit: commit, manifest: manifest},
		{name: "module mismatch", module: "example.test/golem", tag: "go/v1.2.3", commit: commit, manifest: manifest},
		{name: "manifest version mismatch", module: ModulePath, tag: "go/v1.2.4", commit: commit, manifest: manifest},
		{name: "commit mismatch", module: ModulePath, tag: "go/v1.2.3", commit: strings.Repeat("b", 40), manifest: manifest},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if err := ValidateAgreement(mutation.module, mutation.tag, mutation.commit, mutation.manifest); err == nil {
				t.Fatal("release mismatch was accepted")
			}
		})
	}

	// A moving branch or merely annotated tag is never a candidate. The exact
	// refs/tags/go/vX.Y.Z object must pass git's signature verification first.
	repository := t.TempDir()
	moduleDir := filepath.Join(repository, "go")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), "module "+ModulePath+"\n\ngo 1.25.0\n")
	runTestCommand(t, repository, "git", "init", "-q")
	runTestCommand(t, repository, "git", "config", "user.email", "p8-release@example.test")
	runTestCommand(t, repository, "git", "config", "user.name", "P8 Release Test")
	runTestCommand(t, repository, "git", "add", "go/go.mod")
	runTestCommand(t, repository, "git", "commit", "-qm", "candidate")
	runTestCommand(t, repository, "git", "tag", "-a", "go/v1.2.3", "-m", "unsigned candidate")
	if _, err := InspectCandidate(context.Background(), moduleDir, "go/v1.2.3"); errorCode(err) != CodeUnsignedTag {
		t.Fatalf("unsigned candidate error=%v", err)
	}
	if _, err := InspectCandidate(context.Background(), moduleDir, "go/v1.2.4"); errorCode(err) != CodeUnsignedTag {
		t.Fatalf("moving branch/nonexistent tag error=%v", err)
	}

	signedRepository, signedModule, allowedSigners := signedCandidateRepository(t)
	allowedBytes, err := os.ReadFile(allowedSigners)
	if err != nil {
		t.Fatal(err)
	}
	inspect := InspectConfig{ModuleDir: signedModule, Tag: "go/v1.2.3", AllowedSignersFile: allowedSigners, AllowedSignersSHA256: digest(allowedBytes)}
	wrongSignerTrust := inspect
	wrongSignerTrust.AllowedSignersSHA256 = strings.Repeat("0", 64)
	if _, err := InspectCandidateWithConfig(context.Background(), wrongSignerTrust); errorCode(err) != CodeUnsignedTag {
		t.Fatalf("untrusted signer inventory error=%v", err)
	}
	fixtureTemplate, err := os.ReadFile(filepath.Join(signedModule, "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureTrust := compatibility.Digest(fixtureTemplate)
	candidate, err := inspectCandidate(context.Background(), inspect, fixtureTrust)
	if err != nil {
		t.Fatalf("signed candidate rejected: %v", err)
	}
	target := strings.TrimSpace(runTestCommandOutput(t, signedRepository, "git", "rev-parse", "go/v1.2.3^{commit}"))
	materialized, err := compatibility.Parse(candidate.Manifest, compatibility.Digest(candidate.Manifest))
	if err != nil || candidate.Commit != target || ValidateAgreement(ModulePath, candidate.Tag, target, materialized) != nil {
		t.Fatalf("materialized release manifest mismatch candidate=%+v error=%v", candidate, err)
	}
	if candidate.TemplateSHA256 != fixtureTrust {
		t.Fatal("candidate did not bind the separately trusted checked template")
	}

	// The same signed tag cannot authorize a dirty or moving-branch checkout.
	writeTestFile(t, filepath.Join(signedRepository, "moving.txt"), "moving branch\n")
	runTestCommand(t, signedRepository, "git", "add", "moving.txt")
	runTestCommand(t, signedRepository, "git", "commit", "-qm", "moving branch")
	if _, err := inspectCandidate(context.Background(), inspect, fixtureTrust); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("moving branch candidate error=%v", err)
	}
	runTestCommand(t, signedRepository, "git", "checkout", "-q", "go/v1.2.3")
	writeTestFile(t, filepath.Join(signedModule, "compatibility", "manifest.json"), "tampered template\n")
	if _, err := inspectCandidate(context.Background(), inspect, fixtureTrust); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("tampered checked template error=%v", err)
	}
	runTestCommand(t, signedRepository, "git", "restore", "go/compatibility/manifest.json")
	if _, err := inspectCandidate(context.Background(), inspect, strings.Repeat("0", 64)); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("wrong template trust root error=%v", err)
	}
	tamperedRelease := candidate
	tamperedRelease.Manifest = append([]byte(nil), candidate.Manifest...)
	tamperedRelease.Manifest[len(tamperedRelease.Manifest)-2] ^= 1
	if validateCandidate(tamperedRelease) == nil {
		t.Fatal("tampered materialized release manifest retained candidate authority")
	}
	for relative, sealed := range map[string][]byte{
		compatibility.DependencyLicenseAuthorityPath: candidate.licenseAuthority,
		compatibility.ProjectLicensePath:             candidate.projectLicense,
		compatibility.ThirdPartyNoticesPath:          candidate.thirdPartyNotices,
	} {
		tagged := []byte(runTestCommandOutput(t, signedRepository, "git", "show", candidate.Commit+":go/"+relative))
		if !bytes.Equal(tagged, sealed) {
			t.Fatalf("candidate did not seal signed license evidence %s", relative)
		}
	}
	for name, mutate := range map[string]func(*Candidate){
		"authority": func(value *Candidate) { value.licenseAuthority = append(value.licenseAuthority, '\n') },
		"license":   func(value *Candidate) { value.projectLicense = append(value.projectLicense, '\n') },
		"notices":   func(value *Candidate) { value.thirdPartyNotices = append(value.thirdPartyNotices, '\n') },
	} {
		t.Run("sealed-license-"+name, func(t *testing.T) {
			forged := candidate
			forged.licenseAuthority = append([]byte(nil), candidate.licenseAuthority...)
			forged.projectLicense = append([]byte(nil), candidate.projectLicense...)
			forged.thirdPartyNotices = append([]byte(nil), candidate.thirdPartyNotices...)
			mutate(&forged)
			if validateCandidate(forged) == nil {
				t.Fatal("tampered sealed license evidence retained candidate authority")
			}
		})
	}
}

func TestCompatibilityBaselineSelectsGreatestSignedLowerTagAndClosesCompetitors(t *testing.T) {
	repository, _, allowed := signedCandidateRepository(t)
	ctx := context.Background()

	firstRepository, _, firstAllowed := signedCandidateRepository(t)
	tag, version, first, err := greatestLowerReleaseTag(ctx, firstRepository, "go/v1.2.3", "v1.2.3", firstAllowed)
	if err != nil || tag != "" || version != "" || !first {
		t.Fatalf("sole signed release selection=%q/%q first=%t err=%v", tag, version, first, err)
	}

	for _, candidate := range []string{"go/v1.0.0", "go/v1.2.0"} {
		runTestCommand(t, repository, "git", "tag", "-s", candidate, "-m", candidate)
	}
	tag, version, first, err = greatestLowerReleaseTag(ctx, repository, "go/v1.2.3", "v1.2.3", allowed)
	if err != nil || tag != "go/v1.2.0" || version != "v1.2.0" || first {
		t.Fatalf("greatest signed lower release selection=%q/%q first=%t err=%v", tag, version, first, err)
	}

	t.Run("numeric-semver-order", func(t *testing.T) {
		repository, _, allowed := signedCandidateRepository(t)
		for _, candidate := range []string{"go/v0.0.9", "go/v0.0.10"} {
			runTestCommand(t, repository, "git", "tag", "-s", candidate, "-m", candidate)
		}
		tag, version, first, err := greatestLowerReleaseTag(ctx, repository, "go/v1.2.3", "v1.2.3", allowed)
		if err != nil || tag != "go/v0.0.10" || version != "v0.0.10" || first {
			t.Fatalf("numeric semver release selection=%q/%q first=%t err=%v", tag, version, first, err)
		}
	})

	t.Run("unsigned-lower", func(t *testing.T) {
		repository, _, allowed := signedCandidateRepository(t)
		runTestCommand(t, repository, "git", "tag", "go/v1.2.2")
		if _, _, _, err := greatestLowerReleaseTag(ctx, repository, "go/v1.2.3", "v1.2.3", allowed); err == nil {
			t.Fatal("unsigned lower release tag was accepted")
		}
	})
	t.Run("malformed-module-tag", func(t *testing.T) {
		repository, _, allowed := signedCandidateRepository(t)
		runTestCommand(t, repository, "git", "tag", "go/vnot-semver")
		if _, _, _, err := greatestLowerReleaseTag(ctx, repository, "go/v1.2.3", "v1.2.3", allowed); err == nil {
			t.Fatal("malformed module release tag was ignored")
		}
	})
	t.Run("same-or-higher", func(t *testing.T) {
		repository, _, allowed := signedCandidateRepository(t)
		runTestCommand(t, repository, "git", "tag", "-s", "go/v2.0.0", "-m", "higher")
		if _, _, _, err := greatestLowerReleaseTag(ctx, repository, "go/v1.2.3", "v1.2.3", allowed); err == nil {
			t.Fatal("higher module release tag was ignored")
		}
	})
}

func TestSignedCandidateRejectsMissingTamperedAndNonregularLicenseEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "missing-notices", mutate: func(t *testing.T, moduleDir string) {
			if err := os.Remove(filepath.Join(moduleDir, compatibility.ThirdPartyNoticesPath)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "tampered-authority", mutate: func(t *testing.T, moduleDir string) {
			path := filepath.Join(moduleDir, filepath.FromSlash(compatibility.DependencyLicenseAuthorityPath))
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, path, string(bytes.Replace(encoded, []byte(`"v1.52.0"`), []byte(`"v1.52.1"`), 1)))
		}},
		{name: "symlink-project-license", mutate: func(t *testing.T, moduleDir string) {
			path := filepath.Join(moduleDir, compatibility.ProjectLicensePath)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../LICENSE", path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "selected-version-mismatch", mutate: func(t *testing.T, moduleDir string) {
			path := filepath.Join(moduleDir, "go.mod")
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			hostile := bytes.Replace(encoded, []byte("github.com/nats-io/nats.go v1.52.0"), []byte("github.com/nats-io/nats.go v1.52.1"), 1)
			if bytes.Equal(hostile, encoded) {
				t.Fatal("selected NATS version fixture did not change")
			}
			writeTestFile(t, path, string(hostile))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, moduleDir, allowed := signedCandidateRepository(t)
			test.mutate(t, moduleDir)
			runTestCommand(t, repository, "git", "tag", "-d", "go/v1.2.3")
			runTestCommand(t, repository, "git", "add", "-A", "go")
			runTestCommand(t, repository, "git", "commit", "--amend", "-qm", "hostile license evidence")
			runTestCommand(t, repository, "git", "tag", "-s", "go/v1.2.3", "-m", "hostile license evidence")
			allowedBytes, err := os.ReadFile(allowed)
			if err != nil {
				t.Fatal(err)
			}
			template := mustReadPath(t, filepath.Join(moduleDir, "compatibility", "manifest.json"))
			_, err = inspectCandidate(context.Background(), InspectConfig{ModuleDir: moduleDir, Tag: "go/v1.2.3", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}, compatibility.Digest(template))
			if errorCode(err) != CodeInvalidCandidate {
				t.Fatalf("hostile signed license evidence error=%v", err)
			}
		})
	}
}

func TestExactGoV002HistoricalManifestAndCorporaAuthorizeReviewedPreStableMinorTransition(t *testing.T) {
	const historicalManifestSHA256 = "59bd82177890ff594f053ab0cc06f4d1a0b15567d85e673ae6ca563602062c1c"
	fixture, err := os.ReadFile(filepath.Join(moduleRoot(t), "internal", "compatibility", "testdata", "go-v0.0.2-compatibility-manifest-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if compatibility.Digest(fixture) != historicalManifestSHA256 {
		t.Fatalf("checked go/v0.0.2 compatibility manifest digest=%s", compatibility.Digest(fixture))
	}
	if tagged := taggedModuleFile(t, "go/v0.0.2", "compatibility/manifest.json"); !bytes.Equal(tagged, fixture) {
		t.Fatal("checked go/v0.0.2 compatibility manifest differs from exact tagged bytes")
	}

	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	runTestCommand(t, base, "git", "clone", "-q", "--no-local", filepath.Dir(moduleRoot(t)), repository)
	runTestCommand(t, repository, "git", "checkout", "-q", "-b", "candidate", "go/v0.0.2")
	// Keep this historical transition fixture independent of releases created
	// after go/v0.0.2. Otherwise a newer real tag can become the selected
	// baseline (or make the synthetic candidate non-latest) and turn this into
	// a test of the live repository rather than the frozen v0.0.2 boundary.
	for _, tag := range strings.Fields(runTestCommandOutput(t, repository, "git", "tag", "--list", "go/v*")) {
		if tag != "go/v0.0.1" && tag != "go/v0.0.2" {
			runTestCommand(t, repository, "git", "tag", "-d", tag)
		}
	}
	moduleDir := filepath.Join(repository, "go")
	for _, relative := range []string{
		"internal/compatibility/testdata/public-go-api.json",
		"internal/compatibility/testdata/generated-go-abi.json",
		"internal/compatibility/testdata/graphql-abi.json",
		"observe/telemetry-manifest.json",
	} {
		if encoded := taggedModuleFile(t, "go/v0.0.2", relative); len(encoded) == 0 {
			t.Fatalf("empty tagged compatibility evidence %s", relative)
		}
	}
	allowed := configureCandidateSigningOnHistoricalRepository(t, base, repository)
	writeCurrentCompatibilityAuthority(t, moduleDir, true)
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "reviewed current candidate")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v0.1.0", "-m", "reviewed pre-stable minor candidate")
	allowedBytes, err := os.ReadFile(allowed)
	if err != nil {
		t.Fatal(err)
	}
	templateBytes, err := os.ReadFile(filepath.Join(moduleDir, "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := inspectCandidate(context.Background(), InspectConfig{
		ModuleDir: moduleDir, Tag: "go/v0.1.0", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes),
	}, compatibility.Digest(templateBytes))
	if err != nil || validateCandidate(candidate) != nil {
		for _, tag := range []string{"go/v0.0.1", "go/v0.0.2", "go/v0.1.0"} {
			command := exec.Command("git", "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-tag", tag)
			command.Dir = repository
			output, verifyErr := command.CombinedOutput()
			t.Logf("verify %s err=%v output=%s", tag, verifyErr, output)
		}
		t.Fatalf("exact go/v0.0.2 historical transition rejected: %v repository=%s", err, repository)
	}
	if candidate.migrationGuidePath != compatibility.MigrationGuidePath || candidate.migrationGuideSHA256 != compatibility.MigrationGuideSHA256 {
		t.Fatalf("candidate guide authority=%q/%q", candidate.migrationGuidePath, candidate.migrationGuideSHA256)
	}
	taggedGuide := []byte(runTestCommandOutput(t, repository, "git", "show", candidate.Commit+":go/"+compatibility.MigrationGuidePath))
	if !bytes.Equal(candidate.migrationGuide, taggedGuide) {
		t.Fatal("candidate did not detach exact signed migration-guide bytes")
	}
	for name, mutate := range map[string]func(*Candidate){
		"path":   func(value *Candidate) { value.migrationGuidePath = "compatibility/other.json" },
		"digest": func(value *Candidate) { value.migrationGuideSHA256 = strings.Repeat("a", 64) },
		"bytes":  func(value *Candidate) { value.migrationGuide = append(value.migrationGuide, '\n') },
	} {
		t.Run("sealed-"+name, func(t *testing.T) {
			forged := candidate
			forged.migrationGuide = append([]byte(nil), candidate.migrationGuide...)
			mutate(&forged)
			if validateCandidate(forged) == nil {
				t.Fatal("tampered sealed guide retained candidate authority")
			}
		})
	}
}

func TestMigrationGuideTransitionRejectsFirstReleaseAndEverySignedAuthorityTamper(t *testing.T) {
	t.Run("first-release-prior-bound-guide", func(t *testing.T) {
		repository, moduleDir, allowed, _ := signedFirstTransitionRepository(t, false, true)
		allowedBytes, err := os.ReadFile(allowed)
		if err != nil {
			t.Fatal(err)
		}
		template, err := os.ReadFile(filepath.Join(moduleDir, "compatibility", "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inspectCandidate(context.Background(), InspectConfig{ModuleDir: moduleDir, Tag: "go/v1.0.0", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}, compatibility.Digest(template)); errorCode(err) != CodeInvalidCandidate {
			t.Fatalf("first release prior-bound guide error=%v repository=%s", err, repository)
		}
	})

	for _, mutation := range []struct {
		name string
		run  func(t *testing.T, repository, moduleDir string)
	}{
		{name: "missing-guide", run: func(t *testing.T, _, moduleDir string) {
			os.Remove(filepath.Join(moduleDir, "compatibility", "migration-guide-synthetic.json"))
		}},
		{name: "tampered-guide", run: func(t *testing.T, _, moduleDir string) {
			path := filepath.Join(moduleDir, "compatibility", "migration-guide-synthetic.json")
			raw, _ := os.ReadFile(path)
			writeTestFile(t, path, string(append(raw, '\n')))
		}},
		{name: "wrong-path", run: func(t *testing.T, _, moduleDir string) {
			mutateTransitionGuideManifest(t, moduleDir, func(value *compatibility.Manifest) { value.MigrationGuide.Path = "compatibility/missing-guide.json" })
		}},
		{name: "wrong-from", run: func(t *testing.T, _, moduleDir string) {
			mutateTransitionGuideManifest(t, moduleDir, func(value *compatibility.Manifest) { value.MigrationGuide.FromTag = "go/v0.0.1" })
		}},
		{name: "wrong-digest", run: func(t *testing.T, _, moduleDir string) {
			mutateTransitionGuideManifest(t, moduleDir, func(value *compatibility.Manifest) { value.MigrationGuide.SHA256 = strings.Repeat("a", 64) })
		}},
		{name: "wrong-action", run: func(t *testing.T, _, moduleDir string) {
			mutateTransitionGuideManifest(t, moduleDir, func(value *compatibility.Manifest) { value.RequiredActions = append(value.RequiredActions, "zzz.test") })
		}},
		{name: "wrong-commit", run: func(t *testing.T, _, moduleDir string) {
			mutateSyntheticGuide(t, moduleDir, func(value *compatibility.MigrationGuide) { value.From.Commit = strings.Repeat("a", 40) })
		}},
		{name: "wrong-corpus-tree", run: func(t *testing.T, _, moduleDir string) {
			mutateSyntheticGuide(t, moduleDir, func(value *compatibility.MigrationGuide) { value.Corpora[0].GitTree = strings.Repeat("a", 40) })
		}},
		{name: "current-corpus-drift", run: func(t *testing.T, _, moduleDir string) {
			writeTestFile(t, filepath.Join(moduleDir, "internal", "compatibility", "testdata", "p7", "drift.txt"), "drift\n")
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			repository, moduleDir, allowed, _ := signedTransitionRepository(t, "v0.1.0", true, false, false, false)
			runTestCommand(t, repository, "git", "tag", "-d", "go/v0.1.0")
			mutation.run(t, repository, moduleDir)
			runTestCommand(t, repository, "git", "add", "-A", "go")
			runTestCommand(t, repository, "git", "commit", "--amend", "-qm", "tampered candidate")
			runTestCommand(t, repository, "git", "tag", "-s", "go/v0.1.0", "-m", "tampered candidate")
			allowedBytes, err := os.ReadFile(allowed)
			if err != nil {
				t.Fatal(err)
			}
			template, err := os.ReadFile(filepath.Join(moduleDir, "compatibility", "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inspectCandidate(context.Background(), InspectConfig{ModuleDir: moduleDir, Tag: "go/v0.1.0", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}, compatibility.Digest(template)); errorCode(err) != CodeInvalidCandidate {
				t.Fatalf("signed guide tamper accepted: %v repository=%s", err, repository)
			}
		})
	}
}

func configureCandidateSigningOnHistoricalRepository(t *testing.T, base, repository string) string {
	t.Helper()
	runTestCommand(t, repository, "git", "config", "user.email", "p8-release@example.test")
	runTestCommand(t, repository, "git", "config", "user.name", "P8 Release Test")
	key := filepath.Join(base, "candidate-signing-key")
	runTestCommand(t, repository, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	publicKey, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(publicKey))
	if len(fields) < 2 {
		t.Fatal("generated candidate SSH public key is malformed")
	}
	allowed := filepath.Join(base, "historical-and-candidate-allowed-signers")
	const historicalPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFi/wzseXbikqlcCIrJqG7yYMYmVGeLeF1mzWq5dzJ7J"
	writeTestFile(t, allowed, "p8-release@example.test "+fields[0]+" "+fields[1]+"\nroyossai@yahoo.com "+historicalPublicKey+"\n")
	runTestCommand(t, repository, "git", "config", "gpg.format", "ssh")
	runTestCommand(t, repository, "git", "config", "user.signingkey", key)
	runTestCommand(t, repository, "git", "config", "gpg.ssh.allowedSignersFile", allowed)
	return allowed
}

func TestFirstReleaseStillRequiresDigestBoundCanonicalCurrentEvidence(t *testing.T) {
	repository, moduleDir, allowed, trusted := signedMissingEvidenceRepository(t)
	allowedBytes, err := os.ReadFile(allowed)
	if err != nil {
		t.Fatal(err)
	}
	config := InspectConfig{ModuleDir: moduleDir, Tag: "go/v1.2.3", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}
	if _, err := inspectCandidate(context.Background(), config, trusted); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("first release without current corpus evidence error=%v repository=%s", err, repository)
	}
}

func TestP8SignedCompatibilityTransitionRequiresPreStableMinorGuideAndAncestry(t *testing.T) {
	for _, test := range []struct {
		name, version                                   string
		guide, divergent, tamperPrevious, tamperCurrent bool
		wantPass                                        bool
	}{
		{name: "patch-breaking", version: "v0.0.3", guide: true},
		{name: "minor-without-guide", version: "v0.1.0"},
		{name: "minor-reviewed", version: "v0.1.0", guide: true, wantPass: true},
		{name: "tampered-previous-corpus", version: "v0.1.0", guide: true, tamperPrevious: true},
		{name: "tampered-current-corpus", version: "v0.1.0", guide: true, tamperCurrent: true},
		{name: "divergent-signed-baseline", version: "v0.1.0", guide: true, divergent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, moduleDir, allowed, trusted := signedTransitionRepository(t, test.version, test.guide, test.divergent, test.tamperPrevious, test.tamperCurrent)
			allowedBytes, err := os.ReadFile(allowed)
			if err != nil {
				t.Fatal(err)
			}
			config := InspectConfig{ModuleDir: moduleDir, Tag: "go/" + test.version, AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}
			candidate, err := inspectCandidate(context.Background(), config, trusted)
			if test.wantPass {
				if err != nil || validateCandidate(candidate) != nil {
					t.Fatalf("reviewed transition rejected: %v repository=%s", err, repository)
				}
				return
			}
			if errorCode(err) != CodeInvalidCandidate {
				t.Fatalf("unsafe transition error=%v repository=%s", err, repository)
			}
		})
	}
}

func TestPatchReleaseRetainsSignedHistoricalMigrationGuide(t *testing.T) {
	repository, moduleDir, allowed, trusted := signedTransitionRepository(t, "v0.1.0", true, false, false, false)
	runTestCommand(t, repository, "git", "commit", "--allow-empty", "-qm", "patch candidate")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v0.1.1", "-m", "patch candidate")
	allowedBytes, err := os.ReadFile(allowed)
	if err != nil {
		t.Fatal(err)
	}
	config := InspectConfig{ModuleDir: moduleDir, Tag: "go/v0.1.1", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}
	candidate, err := inspectCandidate(context.Background(), config, trusted)
	if err != nil || validateCandidate(candidate) != nil {
		t.Fatalf("patch release did not retain the signed historical guide: %v repository=%s", err, repository)
	}

	runTestCommand(t, repository, "git", "tag", "-d", "go/v0.1.0")
	if _, err := inspectCandidate(context.Background(), config, trusted); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("patch release without signed historical target error=%v repository=%s", err, repository)
	}
}

func TestHistoricalGuideRetentionRefusesNewReleaseLineAndAuthorityDrift(t *testing.T) {
	t.Run("new-release-line", func(t *testing.T) {
		repository, moduleDir, allowed, trusted := signedTransitionRepository(t, "v0.1.0", true, false, false, false)
		runTestCommand(t, repository, "git", "commit", "--allow-empty", "-qm", "new minor candidate")
		runTestCommand(t, repository, "git", "tag", "-s", "go/v0.2.0", "-m", "new minor candidate")
		allowedBytes, err := os.ReadFile(allowed)
		if err != nil {
			t.Fatal(err)
		}
		config := InspectConfig{ModuleDir: moduleDir, Tag: "go/v0.2.0", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}
		if _, err := inspectCandidate(context.Background(), config, trusted); errorCode(err) != CodeInvalidCandidate {
			t.Fatalf("new release line retained an older guide: %v repository=%s", err, repository)
		}
	})

	t.Run("authority-drift", func(t *testing.T) {
		repository, moduleDir, allowed, _ := signedTransitionRepository(t, "v0.1.0", true, false, false, false)
		guide := mustReadPath(t, filepath.Join(moduleDir, compatibility.MigrationGuidePath))
		const retainedPath = "compatibility/retained-migration-guide.json"
		writeTestFile(t, filepath.Join(moduleDir, retainedPath), string(guide))
		manifestBytes := mustReadPath(t, filepath.Join(moduleDir, "compatibility", "manifest.json"))
		manifest, err := compatibility.ParseHistorical(manifestBytes, compatibility.Digest(manifestBytes))
		if err != nil || manifest.MigrationGuide == nil {
			t.Fatalf("parse target manifest: %v", err)
		}
		manifest.MigrationGuide.Path = retainedPath
		manifestBytes, err = compatibility.Encode(manifest)
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(moduleDir, "compatibility", "manifest.json"), string(manifestBytes))
		runTestCommand(t, repository, "git", "add", "go")
		runTestCommand(t, repository, "git", "commit", "-qm", "relabel retained guide")
		runTestCommand(t, repository, "git", "tag", "-s", "go/v0.1.1", "-m", "relabel retained guide")
		allowedBytes, err := os.ReadFile(allowed)
		if err != nil {
			t.Fatal(err)
		}
		config := InspectConfig{ModuleDir: moduleDir, Tag: "go/v0.1.1", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}
		if _, err := inspectCandidate(context.Background(), config, compatibility.Digest(manifestBytes)); errorCode(err) != CodeInvalidCandidate {
			t.Fatalf("patch release changed retained guide authority: %v repository=%s", err, repository)
		}
	})
}

func TestSoleSignedFirstReleaseAcceptsOnlyBoundCurrentEvidence(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "bound", true: "tampered"}[tamper], func(t *testing.T) {
			repository, moduleDir, allowed, trusted := signedFirstTransitionRepository(t, tamper, false)
			allowedBytes, err := os.ReadFile(allowed)
			if err != nil {
				t.Fatal(err)
			}
			config := InspectConfig{ModuleDir: moduleDir, Tag: "go/v1.0.0", AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}
			candidate, err := inspectCandidate(context.Background(), config, trusted)
			if !tamper {
				if err != nil || validateCandidate(candidate) != nil {
					t.Fatalf("bound first release rejected: %v repository=%s", err, repository)
				}
				return
			}
			if errorCode(err) != CodeInvalidCandidate {
				t.Fatalf("tampered first release error=%v repository=%s", err, repository)
			}
		})
	}
}

func TestCLIAndObservationClassificationsRouteThroughReleasePolicy(t *testing.T) {
	observationBytes, err := os.ReadFile(filepath.Join(moduleRoot(t), "observe", "telemetry-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := compatibility.ParseObservationInventory(observationBytes)
	if err != nil {
		t.Fatal(err)
	}
	additive := observation
	additive.Attributes = append(append([]compatibility.ObservationAttribute(nil), observation.Attributes...), compatibility.ObservationAttribute{Name: "golem.release_test", Type: "string"})
	additiveBytes, err := json.Marshal(additive)
	if err != nil {
		t.Fatal(err)
	}
	breaking := observation
	breaking.Coverage = append([]compatibility.ObservationCoverage(nil), observation.Coverage...)
	for index := range breaking.Coverage {
		if len(breaking.Coverage[index].Operations) > 1 {
			operations := append([]string(nil), breaking.Coverage[index].Operations...)
			removed := operations[len(operations)-1]
			breaking.Coverage[index].Operations = operations[:len(operations)-1]
			filtered := breaking.ProviderIndependentOperations[:0]
			for _, operation := range breaking.ProviderIndependentOperations {
				if operation != removed {
					filtered = append(filtered, operation)
				}
			}
			breaking.ProviderIndependentOperations = append([]string(nil), filtered...)
			break
		}
	}
	breakingBytes, err := json.Marshal(breaking)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, version, beforeCLI, afterCLI  string
		beforeObservation, afterObservation []byte
		guide                               bool
		wantPass                            bool
	}{
		{name: "cli-change-patch-rejected", version: "v0.0.3", beforeCLI: strings.Repeat("a", 64), afterCLI: strings.Repeat("b", 64), beforeObservation: observationBytes, afterObservation: observationBytes, guide: true},
		{name: "cli-change-minor-reviewed", version: "v0.1.0", beforeCLI: strings.Repeat("a", 64), afterCLI: strings.Repeat("b", 64), beforeObservation: observationBytes, afterObservation: observationBytes, guide: true, wantPass: true},
		{name: "observation-additive-minor", version: "v0.1.0", beforeCLI: strings.Repeat("a", 64), afterCLI: strings.Repeat("a", 64), beforeObservation: observationBytes, afterObservation: additiveBytes, wantPass: true},
		{name: "observation-breaking-minor-without-guide", version: "v0.1.0", beforeCLI: strings.Repeat("a", 64), afterCLI: strings.Repeat("a", 64), beforeObservation: observationBytes, afterObservation: breakingBytes},
		{name: "observation-breaking-minor-reviewed", version: "v0.1.0", beforeCLI: strings.Repeat("a", 64), afterCLI: strings.Repeat("a", 64), beforeObservation: observationBytes, afterObservation: breakingBytes, guide: true, wantPass: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, moduleDir, allowed, trusted := signedLayerTransitionRepository(t, test.version, test.guide, test.beforeCLI, test.afterCLI, test.beforeObservation, test.afterObservation)
			allowedBytes, err := os.ReadFile(allowed)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := inspectCandidate(context.Background(), InspectConfig{ModuleDir: moduleDir, Tag: "go/" + test.version, AllowedSignersFile: allowed, AllowedSignersSHA256: digest(allowedBytes)}, trusted)
			if test.wantPass {
				if err != nil || validateCandidate(candidate) != nil {
					t.Fatalf("compatible layer transition rejected: %v repository=%s", err, repository)
				}
				return
			}
			if errorCode(err) != CodeInvalidCandidate {
				t.Fatalf("incompatible layer transition error=%v repository=%s", err, repository)
			}
		})
	}
}

func TestCleanConsumerModuleResolutionAndGoInstall(t *testing.T) {
	if testing.Short() {
		t.Fatal("row-22 clean consumer evidence cannot be skipped in short mode")
	}
	version := "v0.0.0"
	proxy := buildLocalModuleProxy(t, moduleRoot(t), version)
	proxyURL := (&url.URL{Scheme: "file", Path: proxy}).String() + ",https://proxy.golang.org"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	temporaryParent := filepath.Join(t.TempDir(), "owned-clean-consumer")
	if err := os.MkdirAll(temporaryParent, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporaryParent)
	if err := VerifyCleanConsumer(ctx, ConsumerConfig{Version: version, Proxy: proxyURL, Hermetic: true}); err != nil {
		var closed *Error
		_ = errors.As(err, &closed)
		t.Fatalf("%v stage=%q", err, closed.stage)
	}
	assertNoCleanConsumerRoots(t, temporaryParent)
}

func TestGoModuleSourceCarriesExactLicenseAndThirdPartyNotices(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "module.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	version := "v1.2.3"
	if err := modzip.CreateFromDir(archive, module.Version{Path: ModulePath, Version: version}, moduleRoot(t)); err != nil {
		_ = archive.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	want := map[string][]byte{
		ModulePath + "@" + version + "/" + compatibility.ProjectLicensePath:    mustReadTestFile(t, compatibility.ProjectLicensePath),
		ModulePath + "@" + version + "/" + compatibility.ThirdPartyNoticesPath: mustReadTestFile(t, compatibility.ThirdPartyNoticesPath),
	}
	for _, entry := range reader.File {
		expected, ok := want[entry.Name]
		if !ok {
			continue
		}
		source, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		actual, readErr := io.ReadAll(source)
		_ = source.Close()
		if readErr != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("module source license %s mismatch: %v", entry.Name, readErr)
		}
		delete(want, entry.Name)
	}
	if len(want) != 0 {
		t.Fatalf("module source archive omitted license evidence: %v", want)
	}
}

func TestReleaseTarAndZipCarryExactThreeEntryLicenseInventory(t *testing.T) {
	entries := []archiveEntry{
		{Name: compatibility.ProjectLicensePath, Mode: 0o644, Content: mustReadTestFile(t, compatibility.ProjectLicensePath)},
		{Name: compatibility.ThirdPartyNoticesPath, Mode: 0o644, Content: mustReadTestFile(t, compatibility.ThirdPartyNoticesPath)},
		{Name: "golem", Mode: 0o755, Content: []byte("exact binary bytes")},
	}
	timestamp := time.Unix(1700000000, 0).UTC()
	for _, format := range []struct {
		name  string
		write func(string, []archiveEntry, time.Time) error
		read  func(*testing.T, string) map[string]archiveEntry
	}{
		{name: "tar.gz", write: writeTarGzip, read: readTarEntries},
		{name: "zip", write: writeZip, read: readZipEntries},
	} {
		t.Run(format.name, func(t *testing.T) {
			first := filepath.Join(t.TempDir(), "first."+format.name)
			second := filepath.Join(t.TempDir(), "second."+format.name)
			if err := format.write(first, entries, timestamp); err != nil {
				t.Fatal(err)
			}
			if err := format.write(second, entries, timestamp); err != nil {
				t.Fatal(err)
			}
			left, err := os.ReadFile(first)
			if err != nil {
				t.Fatal(err)
			}
			right, err := os.ReadFile(second)
			if err != nil || !bytes.Equal(left, right) {
				t.Fatalf("%s archive is not reproducible: %v", format.name, err)
			}
			actual := format.read(t, first)
			if len(actual) != len(entries) {
				t.Fatalf("%s inventory=%v", format.name, actual)
			}
			for _, expected := range entries {
				entry, ok := actual[expected.Name]
				if !ok || entry.Mode != expected.Mode || !bytes.Equal(entry.Content, expected.Content) {
					t.Fatalf("%s entry %s=%+v", format.name, expected.Name, entry)
				}
			}
		})
	}
	if err := writeTarGzip(filepath.Join(t.TempDir(), "unsafe.tar.gz"), []archiveEntry{{Name: "../LICENSE", Mode: 0o644, Content: []byte("x")}}, timestamp); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("unsafe archive inventory error=%v", err)
	}
}

func readTarEntries(t *testing.T, path string) map[string]archiveEntry {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	result := map[string]archiveEntry{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		result[header.Name] = archiveEntry{Name: header.Name, Mode: os.FileMode(header.Mode).Perm(), Content: content}
	}
}

func readZipEntries(t *testing.T, path string) map[string]archiveEntry {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result := map[string]archiveEntry{}
	for _, entry := range reader.File {
		source, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, readErr := io.ReadAll(source)
		_ = source.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		result[entry.Name] = archiveEntry{Name: entry.Name, Mode: entry.Mode().Perm(), Content: content}
	}
	return result
}

func TestCleanConsumerOwnedTemporaryRootIsRemovedOnSuccessAndFailure(t *testing.T) {
	tests := []struct {
		name    string
		failure error
	}{
		{name: "success"},
		{name: "failure", failure: atStage(fail(CodeConsumer), "deliberate-test-failure")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var root string
			err := withCleanConsumerRoot(func(ownedRoot string) error {
				root = ownedRoot
				readOnlyDirectory := filepath.Join(root, "modcache", "cache", "download", "example.test", "module", "@v")
				if err := os.MkdirAll(readOnlyDirectory, 0o755); err != nil {
					t.Fatal(err)
				}
				readOnlyFile := filepath.Join(readOnlyDirectory, "v1.0.0.zip")
				if err := os.WriteFile(readOnlyFile, []byte("owned module cache bytes"), 0o444); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(readOnlyDirectory, 0o555); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Dir(readOnlyDirectory), 0o555); err != nil {
					t.Fatal(err)
				}
				return test.failure
			})
			if root == "" {
				t.Fatal("clean-consumer helper did not expose its owned root to the operation")
			}
			if _, statErr := os.Lstat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("owned clean-consumer root survived cleanup: root=%q error=%v", root, statErr)
			}
			if test.failure == nil {
				if err != nil {
					t.Fatalf("successful operation failed during cleanup: %v", err)
				}
			} else if err != test.failure {
				t.Fatalf("failure cleanup changed the closed operation error: got=%v want=%v", err, test.failure)
			}
		})
	}
}

func TestVerifyCleanConsumerRemovesOwnedRootAfterOperationFailure(t *testing.T) {
	temporaryParent := filepath.Join(t.TempDir(), "owned-clean-consumer")
	if err := os.MkdirAll(temporaryParent, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporaryParent)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := VerifyCleanConsumer(ctx, ConsumerConfig{
		Version:  "v0.0.0",
		Proxy:    (&url.URL{Scheme: "file", Path: filepath.Join(temporaryParent, "missing-proxy")}).String(),
		Hermetic: true,
	})
	var closed *Error
	if !errors.As(err, &closed) || closed.Code != CodeConsumer || closed.stage != "list-module" {
		t.Fatalf("unexpected clean-consumer failure: error=%v closed=%+v", err, closed)
	}
	assertNoCleanConsumerRoots(t, temporaryParent)
}

func assertNoCleanConsumerRoots(t *testing.T, parent string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(parent, "golem-clean-consumer-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("owned clean-consumer roots survived cleanup: %v", matches)
	}
}

func TestInstalledCLIVersionDocumentIsStrictAndBindsCandidateABI(t *testing.T) {
	valid, err := json.Marshal(map[string]any{
		"formatVersion": FormatVersion,
		"module":        ModulePath,
		"version":       "v1.2.3",
		"commit":        strings.Repeat("0", 40),
		"generatorABI":  manifest.GeneratorVersion,
		"runtimeABI":    manifest.TemplateABIVersion,
	})
	if err != nil || !validInstalledVersion(valid, "v1.2.3") {
		t.Fatal("exact installed CLI version document was rejected")
	}
	mutations := [][]byte{
		append(append([]byte(nil), valid...), []byte("{}")...),
		bytes.Replace(valid, []byte(`"version":"v1.2.3"`), []byte(`"version":"v1.2.4"`), 1),
		bytes.Replace(valid, []byte(`"commit":"`+strings.Repeat("0", 40)+`"`), []byte(`"commit":"`+strings.Repeat("a", 40)+`"`), 1),
		bytes.Replace(valid, []byte(`"generatorABI":"`+manifest.GeneratorVersion+`"`), []byte(`"generatorABI":"unknown"`), 1),
		bytes.Replace(valid, []byte(`"runtimeABI":"`+manifest.TemplateABIVersion+`"`), []byte(`"runtimeABI":"unknown"`), 1),
		bytes.Replace(valid, []byte(`"module":"`+ModulePath+`"`), []byte(`"module":"example.test/other"`), 1),
		bytes.Replace(valid, []byte(`"formatVersion":1`), []byte(`"formatVersion":1,"unknown":true`), 1),
	}
	for index, mutation := range mutations {
		if validInstalledVersion(mutation, "v1.2.3") {
			t.Fatalf("invalid installed CLI version mutation %d was accepted: %s", index, mutation)
		}
	}
}

func TestReleaseArtifactReproducibility(t *testing.T) {
	if testing.Short() {
		t.Fatal("row-22 reproducibility evidence cannot be skipped in short mode")
	}
	_, releaseModule, allowedSigners := signedFullCandidateRepository(t)
	allowedBytes, err := os.ReadFile(allowedSigners)
	if err != nil {
		t.Fatal(err)
	}
	templateBytes, err := os.ReadFile(filepath.Join(releaseModule, "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := inspectCandidate(context.Background(), InspectConfig{ModuleDir: releaseModule, Tag: "go/v1.2.3", AllowedSignersFile: allowedSigners, AllowedSignersSHA256: digest(allowedBytes)}, compatibility.Digest(templateBytes))
	if err != nil {
		t.Fatal(err)
	}
	platform := Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if _, err := Build(ctx, BuildConfig{ModuleDir: releaseModule, OutputDir: first, Candidate: candidate, Platforms: []Platform{platform}}); err != nil {
		var closed *Error
		_ = errors.As(err, &closed)
		t.Fatalf("%v stage=%q", err, closed.stage)
	}
	if _, err := Build(ctx, BuildConfig{ModuleDir: releaseModule, OutputDir: second, Candidate: candidate, Platforms: []Platform{platform}}); err != nil {
		t.Fatal(err)
	}
	equal, err := equalDirectories(first, second)
	if err != nil || !equal {
		t.Fatalf("independent release builds differ: equal=%t error=%v", equal, err)
	}
	assertReleaseSchemasAndDigests(t, first, candidate)
	firstFiles, _ := readDirectory(first)
	for name, content := range firstFiles {
		if bytes.Contains(content, []byte(releaseModule)) || bytes.Contains(content, []byte(t.TempDir())) {
			t.Fatalf("artifact %s contains an absolute build path", name)
		}
	}
	guidePath := filepath.Join(first, filepath.FromSlash(candidate.migrationGuidePath))
	if err := os.Remove(guidePath); err != nil {
		t.Fatal(err)
	}
	if err := Publish(first, filepath.Join(t.TempDir(), "releases"), candidate); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("publish without signed guide error=%v", err)
	}
	outside := filepath.Join(t.TempDir(), "guide.json")
	writeTestFile(t, outside, string(candidate.migrationGuide))
	if err := os.Symlink(outside, guidePath); err != nil {
		t.Fatal(err)
	}
	if err := Publish(first, filepath.Join(t.TempDir(), "releases"), candidate); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("publish with symlinked guide error=%v", err)
	}
}

func TestExistingVersionArtifactReplacementRefused(t *testing.T) {
	candidate := testCandidate(t)
	staged := filepath.Join(t.TempDir(), "staged")
	writeTestFile(t, filepath.Join(staged, "golem.tar.gz"), "immutable artifact")
	writeTestFile(t, filepath.Join(staged, "SHA256SUMS"), "immutable checksum")
	writeTestFile(t, filepath.Join(staged, compatibility.ProjectLicensePath), string(candidate.projectLicense))
	writeTestFile(t, filepath.Join(staged, compatibility.ThirdPartyNoticesPath), string(candidate.thirdPartyNotices))
	releases := filepath.Join(t.TempDir(), "releases")
	if err := Publish(staged, releases, candidate); err != nil {
		t.Fatal(err)
	}
	if err := Publish(staged, releases, candidate); err != nil {
		t.Fatalf("byte-identical retry was not idempotent: %v", err)
	}
	if err := os.Remove(filepath.Join(staged, compatibility.ThirdPartyNoticesPath)); err != nil {
		t.Fatal(err)
	}
	if err := Publish(staged, filepath.Join(t.TempDir(), "missing-notice-releases"), candidate); errorCode(err) != CodeInvalidCandidate {
		t.Fatalf("publish without sealed third-party notices error=%v", err)
	}
	writeTestFile(t, filepath.Join(staged, compatibility.ThirdPartyNoticesPath), string(candidate.thirdPartyNotices))
	writeTestFile(t, filepath.Join(staged, "golem.tar.gz"), "different bytes")
	if err := Publish(staged, releases, candidate); errorCode(err) != CodeReplacement {
		t.Fatalf("existing version replacement error=%v", err)
	}
	writeTestFile(t, filepath.Join(staged, "golem.tar.gz"), "immutable artifact")
	writeTestFile(t, filepath.Join(staged, "unexpected"), "new path")
	if err := Publish(staged, releases, candidate); errorCode(err) != CodeReplacement {
		t.Fatalf("existing version path addition error=%v", err)
	}
}

func assertReleaseSchemasAndDigests(t *testing.T, root string, candidate Candidate) {
	t.Helper()
	for relative, expected := range map[string][]byte{
		compatibility.ProjectLicensePath:    candidate.projectLicense,
		compatibility.ThirdPartyNoticesPath: candidate.thirdPartyNotices,
	} {
		actual, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("staged license evidence %s differs: %v", relative, err)
		}
	}
	assertReleaseArchiveLicenseEvidence(t, root, candidate)
	if len(candidate.migrationGuide) != 0 {
		actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(candidate.migrationGuidePath)))
		if err != nil || !bytes.Equal(actual, candidate.migrationGuide) || digest(actual) != candidate.migrationGuideSHA256 {
			t.Fatalf("built migration guide differs from signed candidate: err=%v", err)
		}
		var inventory Inventory
		decodeStrict(t, filepath.Join(root, "release-inventory.json"), &inventory)
		found := false
		for _, item := range inventory.Files {
			if item.Name == candidate.migrationGuidePath && item.SHA256 == candidate.migrationGuideSHA256 {
				found = true
			}
		}
		if !found {
			t.Fatalf("release inventory omitted signed migration guide: %+v", inventory.Files)
		}
	}
	type independentChecksum struct {
		Algorithm     string `json:"algorithm"`
		ChecksumValue string `json:"checksumValue"`
	}
	type independentPackage struct {
		SPDXID           string                `json:"SPDXID"`
		Name             string                `json:"name"`
		VersionInfo      string                `json:"versionInfo"`
		DownloadLocation string                `json:"downloadLocation"`
		FilesAnalyzed    bool                  `json:"filesAnalyzed"`
		LicenseConcluded string                `json:"licenseConcluded"`
		LicenseDeclared  string                `json:"licenseDeclared"`
		CopyrightText    string                `json:"copyrightText"`
		Checksums        []independentChecksum `json:"checksums"`
	}
	type independentRelationship struct {
		SPDXElementID      string `json:"spdxElementId"`
		RelationshipType   string `json:"relationshipType"`
		RelatedSPDXElement string `json:"relatedSpdxElement"`
	}
	type independentSPDX struct {
		SPDXVersion       string `json:"spdxVersion"`
		DataLicense       string `json:"dataLicense"`
		SPDXID            string `json:"SPDXID"`
		Name              string `json:"name"`
		DocumentNamespace string `json:"documentNamespace"`
		CreationInfo      struct {
			Created            string   `json:"created"`
			Creators           []string `json:"creators"`
			LicenseListVersion string   `json:"licenseListVersion"`
		} `json:"creationInfo"`
		DocumentDescribes []string                  `json:"documentDescribes"`
		Packages          []independentPackage      `json:"packages"`
		Relationships     []independentRelationship `json:"relationships"`
		ExtractedLicenses []struct {
			LicenseID     string `json:"licenseId"`
			ExtractedText string `json:"extractedText"`
			Name          string `json:"name"`
		} `json:"hasExtractedLicensingInfos"`
	}
	var source independentSPDX
	decodeStrict(t, filepath.Join(root, "golem_source.spdx.json"), &source)
	if source.SPDXVersion != "SPDX-2.3" || source.DataLicense != "CC0-1.0" || source.SPDXID != "SPDXRef-DOCUMENT" || source.DocumentNamespace == "" || len(source.CreationInfo.Creators) == 0 || len(source.Packages) == 0 {
		t.Fatalf("source SPDX required fields=%+v", source)
	}
	if want := time.Unix(candidate.SourceDateEpoch, 0).UTC().Format(time.RFC3339); source.CreationInfo.Created != want || strings.HasPrefix(source.CreationInfo.Created, "2000-") {
		t.Fatalf("source SPDX creation=%q want truthful tag-commit time %q", source.CreationInfo.Created, want)
	}
	for _, pkg := range source.Packages {
		if pkg.SPDXID == "" || pkg.Name == "" || len(pkg.Checksums) != 1 || pkg.Checksums[0].Algorithm != "SHA256" || len(pkg.Checksums[0].ChecksumValue) != 64 {
			t.Fatalf("invalid source SPDX package=%+v", pkg)
		}
		if pkg.Name == ModulePath && pkg.Checksums[0].ChecksumValue != candidate.SourceTreeSHA256 {
			t.Fatalf("main source SPDX checksum=%s want tree=%s", pkg.Checksums[0].ChecksumValue, candidate.SourceTreeSHA256)
		}
	}
	authority, err := compatibility.ParseDependencyLicenseAuthority(candidate.licenseAuthority, compatibility.DependencyLicenseAuthoritySHA256)
	if err != nil {
		t.Fatal(err)
	}
	wantDeclared := map[string]string{authority.Project.Module: authority.Project.LicenseDeclared}
	for _, dependency := range authority.Dependencies {
		wantDeclared[dependency.Module] = dependency.LicenseDeclared
	}
	for module, declared := range wantDeclared {
		found := false
		for _, pkg := range source.Packages {
			if pkg.Name == module {
				found = true
				if pkg.LicenseDeclared != declared || pkg.LicenseConcluded != "NOASSERTION" {
					t.Fatalf("source SPDX license %s=%q/%q want %q/NOASSERTION", module, pkg.LicenseDeclared, pkg.LicenseConcluded, declared)
				}
			}
		}
		if !found {
			t.Fatalf("source SPDX omitted governed module %s", module)
		}
	}
	if len(source.ExtractedLicenses) != 2 || source.ExtractedLicenses[0].LicenseID != compatibility.ProjectLicenseDeclared || source.ExtractedLicenses[0].ExtractedText != string(candidate.projectLicense) || source.ExtractedLicenses[1].LicenseID != "LicenseRef-Klauspost-Compress-Composite" {
		t.Fatalf("source SPDX extracted licenses=%+v", source.ExtractedLicenses)
	}
	authorityLicense, ok := compatibility.DependencyLicenseText(candidate.thirdPartyNotices, authority.Dependencies[0])
	if !ok || source.ExtractedLicenses[1].ExtractedText != string(authorityLicense) {
		t.Fatal("source SPDX composite LicenseRef did not retain exact reviewed text")
	}
	binarySBOMs, err := filepath.Glob(filepath.Join(root, "golem_*_*.spdx.json"))
	if err != nil || len(binarySBOMs) != 1 {
		t.Fatalf("binary SPDX inventory=%v error=%v", binarySBOMs, err)
	}
	var binary independentSPDX
	decodeStrict(t, binarySBOMs[0], &binary)
	if binary.SPDXVersion != "SPDX-2.3" || binary.DataLicense != "CC0-1.0" || binary.SPDXID != "SPDXRef-DOCUMENT" || len(binary.Packages) != 1 || len(binary.Packages[0].Checksums) != 1 || len(binary.Packages[0].Checksums[0].ChecksumValue) != 64 || binary.Packages[0].LicenseDeclared != compatibility.ProjectLicenseDeclared || binary.Packages[0].LicenseConcluded != "NOASSERTION" || len(binary.ExtractedLicenses) != 1 || binary.ExtractedLicenses[0].LicenseID != compatibility.ProjectLicenseDeclared || binary.ExtractedLicenses[0].ExtractedText != string(candidate.projectLicense) {
		t.Fatalf("invalid binary SPDX=%+v", binary)
	}
	var statement struct {
		Type          string `json:"_type"`
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
		Predicate struct {
			BuildDefinition struct {
				BuildType          string `json:"buildType"`
				ExternalParameters struct {
					Module    string   `json:"module"`
					Version   string   `json:"version"`
					Tag       string   `json:"tag"`
					Commit    string   `json:"commit"`
					Platforms []string `json:"platforms"`
				} `json:"externalParameters"`
				InternalParameters   map[string]any `json:"internalParameters"`
				ResolvedDependencies []struct {
					URI    string            `json:"uri"`
					Digest map[string]string `json:"digest"`
				} `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
			RunDetails struct {
				Builder struct {
					ID string `json:"id"`
				} `json:"builder"`
				Metadata struct {
					InvocationID string `json:"invocationId"`
				} `json:"metadata"`
			} `json:"runDetails"`
		} `json:"predicate"`
	}
	decodeStrict(t, filepath.Join(root, "provenance.json"), &statement)
	if statement.Type != "https://in-toto.io/Statement/v1" || statement.PredicateType != "https://slsa.dev/provenance/v1" || statement.Predicate.BuildDefinition.BuildType == "" || statement.Predicate.BuildDefinition.ExternalParameters.Module != ModulePath || statement.Predicate.BuildDefinition.ExternalParameters.Tag != candidate.Tag || statement.Predicate.BuildDefinition.ExternalParameters.Commit != candidate.Commit || statement.Predicate.RunDetails.Builder.ID == "" || statement.Predicate.RunDetails.Metadata.InvocationID == "" {
		t.Fatalf("invalid in-toto/SLSA statement=%+v", statement)
	}
	wantDependencies := 7
	if len(candidate.migrationGuide) != 0 {
		wantDependencies++
	}
	if len(statement.Predicate.BuildDefinition.ResolvedDependencies) != wantDependencies {
		t.Fatalf("provenance dependencies=%+v", statement.Predicate.BuildDefinition.ResolvedDependencies)
	}
	resolved := map[string]map[string]string{}
	for _, dependency := range statement.Predicate.BuildDefinition.ResolvedDependencies {
		resolved[dependency.URI] = dependency.Digest
	}
	if resolved["file:compatibility/manifest.json"]["sha256"] != candidate.TemplateSHA256 || resolved["git+https://github.com/eleven-am/golem.git@"+candidate.Tag+"#subdirectory=go"]["sha256"] != candidate.SourceTreeSHA256 || resolved["urn:golem:release-allowed-signers"]["sha256"] != candidate.SignersSHA256 {
		t.Fatalf("provenance omitted trusted template/source tree: %+v", resolved)
	}
	if resolved["file:"+compatibility.DependencyLicenseAuthorityPath]["sha256"] != compatibility.DependencyLicenseAuthoritySHA256 || resolved["file:"+compatibility.ProjectLicensePath]["sha256"] != digest(candidate.projectLicense) || resolved["file:"+compatibility.ThirdPartyNoticesPath]["sha256"] != digest(candidate.thirdPartyNotices) {
		t.Fatalf("provenance omitted sealed license evidence: %+v", resolved)
	}
	if len(candidate.migrationGuide) != 0 && resolved["file:"+candidate.migrationGuidePath]["sha256"] != candidate.migrationGuideSHA256 {
		t.Fatalf("provenance omitted signed migration guide: %+v", resolved)
	}
	for _, subject := range statement.Subject {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(subject.Name)))
		if err != nil || subject.Digest["sha256"] != digest(content) {
			t.Fatalf("provenance subject %s digest mismatch", subject.Name)
		}
	}
	checksums, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(checksums)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid checksum line %q", line)
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(parts[1])))
		if err != nil || parts[0] != digest(content) {
			t.Fatalf("checksum mismatch %q", line)
		}
	}
	notes, err := os.ReadFile(filepath.Join(root, "RELEASE_NOTES.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{candidate.Version, candidate.Tag, candidate.Commit, compatibility.Digest(candidate.Manifest), "publicGoAPI", "generatedTemplateABI", "Required actions", "Known boundaries", "cdc.requires-adapter", "mysql.unsupported"} {
		if !bytes.Contains(notes, []byte(required)) {
			t.Fatalf("release notes omitted %q: %s", required, notes)
		}
	}
	if bytes.Contains(notes, []byte("2000-01-01")) || bytes.Contains(notes, []byte(moduleRoot(t))) {
		t.Fatal("release notes contain fabricated time or build path")
	}
}

func assertReleaseArchiveLicenseEvidence(t *testing.T, root string, candidate Candidate) {
	t.Helper()
	archives, err := filepath.Glob(filepath.Join(root, "golem_*"))
	if err != nil {
		t.Fatal(err)
	}
	var archivePath string
	for _, path := range archives {
		if strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".zip") {
			archivePath = path
			break
		}
	}
	if archivePath == "" {
		t.Fatal("release archive missing")
	}
	want := map[string]struct {
		mode os.FileMode
		data []byte
	}{
		compatibility.ProjectLicensePath:    {mode: 0o644, data: candidate.projectLicense},
		compatibility.ThirdPartyNoticesPath: {mode: 0o644, data: candidate.thirdPartyNotices},
	}
	found := map[string]bool{}
	var names []string
	var binaryName string
	var binaryBytes []byte
	if strings.HasSuffix(archivePath, ".zip") {
		reader, err := zip.OpenReader(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		for _, entry := range reader.File {
			names = append(names, entry.Name)
			if expected, ok := want[entry.Name]; ok {
				source, err := entry.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := io.ReadAll(source)
				_ = source.Close()
				if readErr != nil || !bytes.Equal(data, expected.data) || entry.Mode().Perm() != expected.mode {
					t.Fatalf("zip license entry %s mode=%o error=%v", entry.Name, entry.Mode().Perm(), readErr)
				}
				found[entry.Name] = true
			} else {
				if entry.Name != "golem.exe" || entry.Mode().Perm() != 0o755 {
					t.Fatalf("unexpected zip archive entry %s mode=%o", entry.Name, entry.Mode().Perm())
				}
				source, err := entry.Open()
				if err != nil {
					t.Fatal(err)
				}
				binaryBytes, err = io.ReadAll(source)
				_ = source.Close()
				if err != nil || len(binaryBytes) == 0 {
					t.Fatalf("zip binary bytes: %v", err)
				}
				binaryName = entry.Name
			}
		}
	} else {
		file, err := os.Open(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		compressed, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		defer compressed.Close()
		reader := tar.NewReader(compressed)
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, header.Name)
			if expected, ok := want[header.Name]; ok {
				data, readErr := io.ReadAll(reader)
				if readErr != nil || !bytes.Equal(data, expected.data) || os.FileMode(header.Mode).Perm() != expected.mode {
					t.Fatalf("tar license entry %s mode=%o error=%v", header.Name, header.Mode, readErr)
				}
				found[header.Name] = true
			} else {
				if header.Name != "golem" || os.FileMode(header.Mode).Perm() != 0o755 {
					t.Fatalf("unexpected tar archive entry %s mode=%o", header.Name, header.Mode)
				}
				binaryBytes, err = io.ReadAll(reader)
				if err != nil || len(binaryBytes) == 0 {
					t.Fatalf("tar binary bytes: %v", err)
				}
				binaryName = header.Name
			}
		}
	}
	for relative := range want {
		if !found[relative] {
			t.Fatalf("release archive omitted %s", relative)
		}
	}
	wantNames := []string{compatibility.ProjectLicensePath, compatibility.ThirdPartyNoticesPath, binaryName}
	if len(names) != 3 || strings.Join(names, "\x00") != strings.Join(wantNames, "\x00") {
		t.Fatalf("release archive inventory=%v want %v", names, wantNames)
	}
	base := strings.TrimSuffix(filepath.Base(archivePath), ".tar.gz")
	base = strings.TrimSuffix(base, ".zip")
	var sbom struct {
		Packages []struct {
			Checksums []struct {
				Algorithm, ChecksumValue string
			} `json:"checksums"`
		} `json:"packages"`
	}
	encoded, err := os.ReadFile(filepath.Join(root, base+".spdx.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &sbom); err != nil {
		t.Fatalf("read archive binary SPDX: %v", err)
	}
	if len(sbom.Packages) != 1 || len(sbom.Packages[0].Checksums) != 1 || sbom.Packages[0].Checksums[0].Algorithm != "SHA256" || sbom.Packages[0].Checksums[0].ChecksumValue != digest(binaryBytes) {
		t.Fatalf("archive binary %s bytes not bound by SPDX: %+v", binaryName, sbom)
	}
}

func decodeStrict(t *testing.T, path string, destination any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode %s: %v", filepath.Base(path), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s trailing content: %v", filepath.Base(path), err)
	}
}

func buildLocalModuleProxy(t *testing.T, source, version string) string {
	t.Helper()
	proxy := t.TempDir()
	escaped, err := module.EscapePath(ModulePath)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(proxy, filepath.FromSlash(escaped), "@v")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join(source, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, version+".mod"), string(goMod))
	info, _ := json.Marshal(map[string]any{"Version": version, "Time": time.Unix(1700000000, 0).UTC()})
	writeTestFile(t, filepath.Join(root, version+".info"), string(info)+"\n")
	archive, err := os.Create(filepath.Join(root, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(archive, module.Version{Path: ModulePath, Version: version}, source); err != nil {
		_ = archive.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "list"), version+"\n")
	return proxy
}

func testCandidate(t *testing.T) Candidate {
	t.Helper()
	commit := strings.Repeat("c", 40)
	manifest := compatibility.DevelopmentManifest()
	manifest.MigrationGuide = nil
	manifest.RequiredActions = []string{"migrate.database", "regenerate.generated"}
	manifest.Release = compatibility.Release{Version: "v1.2.3", Tag: "go/v1.2.3", Commit: commit}
	encoded, err := compatibility.Encode(manifest)
	if err != nil {
		panic(err)
	}
	template := compatibility.DevelopmentManifest()
	template.MigrationGuide = nil
	template.RequiredActions = []string{"migrate.database", "regenerate.generated"}
	templateBytes, err := compatibility.Encode(template)
	if err != nil {
		panic(err)
	}
	return sealCandidate(Candidate{Module: ModulePath, Version: "v1.2.3", Tag: "go/v1.2.3", Commit: commit, Manifest: encoded, TemplateSHA256: compatibility.Digest(templateBytes), SourceTreeSHA256: strings.Repeat("d", 64), SignersSHA256: strings.Repeat("e", 64), SourceDateEpoch: 1700000000, licenseAuthority: mustReadTestFile(t, compatibility.DependencyLicenseAuthorityPath), projectLicense: mustReadTestFile(t, compatibility.ProjectLicensePath), thirdPartyNotices: mustReadTestFile(t, compatibility.ThirdPartyNoticesPath)})
}

func signedCandidateRepository(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	moduleDir := filepath.Join(repository, "go")
	if err := os.MkdirAll(filepath.Join(moduleDir, "compatibility"), 0o755); err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join(moduleRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), string(goMod))
	writeCurrentCompatibilityAuthority(t, moduleDir, false)
	allowed := signPreparedRepository(t, base, repository)
	return repository, moduleDir, allowed
}

func signedFullCandidateRepository(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	moduleDir := filepath.Join(repository, "go")
	archivePath := filepath.Join(base, "module.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(archive, module.Version{Path: ModulePath, Version: "v1.2.3"}, moduleRoot(t)); err != nil {
		_ = archive.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	prefix := ModulePath + "@v1.2.3/"
	for _, entry := range reader.File {
		if !strings.HasPrefix(entry.Name, prefix) || strings.HasSuffix(entry.Name, "/") {
			continue
		}
		relative := strings.TrimPrefix(entry.Name, prefix)
		destination := filepath.Join(moduleDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		source, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(source)
		_ = source.Close()
		writeErr := os.WriteFile(destination, content, 0o644)
		if err != nil || writeErr != nil {
			t.Fatalf("extract module file %s: %v", relative, err)
		}
	}
	_ = reader.Close()
	writeCurrentCompatibilityAuthority(t, moduleDir, false)
	allowed := configureTransitionSigning(t, base, repository)
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "full baseline")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v0.0.2", "-m", "full baseline")
	previousCommit := strings.TrimSpace(runTestCommandOutput(t, repository, "git", "rev-parse", "go/v0.0.2^{commit}"))
	writeSyntheticTransitionGuide(t, repository, moduleDir, previousCommit, "v1.2.3")
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "full signed candidate")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v1.2.3", "-m", "full signed candidate")
	return repository, moduleDir, allowed
}

func writeCurrentCompatibilityAuthority(t *testing.T, moduleDir string, includeGuide bool) {
	t.Helper()
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), string(mustReadTestFile(t, "go.mod")))
	for _, relative := range []string{
		compatibility.MigrationGuidePath,
		compatibility.DependencyLicenseAuthorityPath,
		compatibility.ProjectLicensePath,
		compatibility.ThirdPartyNoticesPath,
		"internal/compatibility/testdata/public-go-api.json",
		"internal/compatibility/testdata/generated-go-abi.json",
		"internal/compatibility/testdata/graphql-abi.json",
		"observe/telemetry-manifest.json",
	} {
		encoded, err := os.ReadFile(filepath.Join(moduleRoot(t), filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read current compatibility authority %s: %v", relative, err)
		}
		writeTestFile(t, filepath.Join(moduleDir, filepath.FromSlash(relative)), string(encoded))
	}
	value := compatibility.DevelopmentManifest()
	if !includeGuide {
		value.MigrationGuide = nil
		value.RequiredActions = []string{"migrate.database", "regenerate.generated"}
	}
	for target, relative := range map[*string]string{
		&value.Digests.PublicGoAPI:    "internal/compatibility/testdata/public-go-api.json",
		&value.Digests.GeneratedGoABI: "internal/compatibility/testdata/generated-go-abi.json",
		&value.Digests.GraphQLABI:     "internal/compatibility/testdata/graphql-abi.json",
		&value.Digests.Observation:    "observe/telemetry-manifest.json",
	} {
		encoded, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		*target = compatibility.Digest(encoded)
	}
	encoded, err := compatibility.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "compatibility", "manifest.json"), string(encoded))
}

func taggedModuleFile(t *testing.T, tag, relative string) []byte {
	t.Helper()
	command := exec.Command("git", "show", tag+":go/"+relative)
	command.Dir = moduleRoot(t)
	encoded, err := command.Output()
	if err != nil {
		t.Fatalf("read %s go/%s: %v", tag, relative, err)
	}
	return encoded
}

func signPreparedRepository(t *testing.T, base, repository string) string {
	t.Helper()
	runTestCommand(t, repository, "git", "init", "-q")
	runTestCommand(t, repository, "git", "config", "user.email", "p8-release@example.test")
	runTestCommand(t, repository, "git", "config", "user.name", "P8 Release Test")
	key := filepath.Join(base, "signing-key")
	runTestCommand(t, repository, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	publicKey, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(base, "allowed-signers")
	keyFields := strings.Fields(string(publicKey))
	if len(keyFields) < 2 {
		t.Fatal("generated SSH public key is malformed")
	}
	writeTestFile(t, allowed, "p8-release@example.test "+keyFields[0]+" "+keyFields[1]+"\n")
	runTestCommand(t, repository, "git", "config", "gpg.format", "ssh")
	runTestCommand(t, repository, "git", "config", "user.signingkey", key)
	runTestCommand(t, repository, "git", "config", "gpg.ssh.allowedSignersFile", allowed)
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "signed candidate")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v1.2.3", "-m", "signed candidate")
	return allowed
}

func signedTransitionRepository(t *testing.T, currentVersion string, guide, divergent, tamperPrevious, tamperCurrent bool) (string, string, string, string) {
	t.Helper()
	base, repository := t.TempDir(), ""
	repository = filepath.Join(base, "repository")
	moduleDir := filepath.Join(repository, "go")
	allowed := configureTransitionSigning(t, base, repository)
	writeTransitionState(t, moduleDir, "struct{Value string}", true, tamperPrevious)
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "baseline")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v0.0.2", "-m", "baseline")
	previousCommit := strings.TrimSpace(runTestCommandOutput(t, repository, "git", "rev-parse", "go/v0.0.2^{commit}"))
	if divergent {
		runTestCommand(t, repository, "git", "checkout", "--orphan", "candidate")
		runTestCommand(t, repository, "git", "rm", "-qrf", ".")
	}
	writeTransitionState(t, moduleDir, "struct{Value int}", guide, tamperCurrent)
	if guide {
		writeSyntheticTransitionGuide(t, repository, moduleDir, previousCommit, currentVersion)
	}
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "candidate")
	runTestCommand(t, repository, "git", "tag", "-s", "go/"+currentVersion, "-m", "candidate")
	template, err := os.ReadFile(filepath.Join(moduleDir, "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return repository, moduleDir, allowed, compatibility.Digest(template)
}

func signedFirstTransitionRepository(t *testing.T, tamper, guide bool) (string, string, string, string) {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	moduleDir := filepath.Join(repository, "go")
	allowed := configureTransitionSigning(t, base, repository)
	writeTransitionState(t, moduleDir, "struct{Value string}", guide, tamper)
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "first candidate")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v1.0.0", "-m", "first candidate")
	template, err := os.ReadFile(filepath.Join(moduleDir, "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return repository, moduleDir, allowed, compatibility.Digest(template)
}

func signedMissingEvidenceRepository(t *testing.T) (string, string, string, string) {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	moduleDir := filepath.Join(repository, "go")
	allowed := configureTransitionSigning(t, base, repository)
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), string(mustReadTestFile(t, "go.mod")))
	for _, relative := range []string{compatibility.DependencyLicenseAuthorityPath, compatibility.ProjectLicensePath, compatibility.ThirdPartyNoticesPath} {
		writeTestFile(t, filepath.Join(moduleDir, filepath.FromSlash(relative)), string(mustReadTestFile(t, relative)))
	}
	value := compatibility.DevelopmentManifest()
	value.MigrationGuide = nil
	value.RequiredActions = []string{"migrate.database", "regenerate.generated"}
	template, err := compatibility.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "compatibility", "manifest.json"), string(template))
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "missing compatibility evidence")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v1.2.3", "-m", "missing compatibility evidence")
	return repository, moduleDir, allowed, compatibility.Digest(template)
}

func signedLayerTransitionRepository(t *testing.T, currentVersion string, guide bool, beforeCLI, afterCLI string, beforeObservation, afterObservation []byte) (string, string, string, string) {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	moduleDir := filepath.Join(repository, "go")
	allowed := configureTransitionSigning(t, base, repository)
	writeTransitionEvidence(t, moduleDir, "struct{Value string}", false, false, beforeCLI, beforeObservation)
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "baseline")
	runTestCommand(t, repository, "git", "tag", "-s", "go/v0.0.2", "-m", "baseline")
	previousCommit := strings.TrimSpace(runTestCommandOutput(t, repository, "git", "rev-parse", "go/v0.0.2^{commit}"))
	writeTransitionEvidence(t, moduleDir, "struct{Value string}", guide, false, afterCLI, afterObservation)
	if guide {
		writeSyntheticTransitionGuide(t, repository, moduleDir, previousCommit, currentVersion)
	}
	runTestCommand(t, repository, "git", "add", "go")
	runTestCommand(t, repository, "git", "commit", "-qm", "candidate")
	runTestCommand(t, repository, "git", "tag", "-s", "go/"+currentVersion, "-m", "candidate")
	template, err := os.ReadFile(filepath.Join(moduleDir, "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return repository, moduleDir, allowed, compatibility.Digest(template)
}

func configureTransitionSigning(t *testing.T, base, repository string) string {
	t.Helper()
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, repository, "git", "init", "-q")
	runTestCommand(t, repository, "git", "config", "user.email", "p8-release@example.test")
	runTestCommand(t, repository, "git", "config", "user.name", "P8 Release Test")
	key := filepath.Join(base, "transition-signing-key")
	runTestCommand(t, repository, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	publicKey, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(publicKey))
	if len(fields) < 2 {
		t.Fatal("generated SSH public key is malformed")
	}
	allowed := filepath.Join(base, "transition-allowed-signers")
	writeTestFile(t, allowed, "p8-release@example.test "+fields[0]+" "+fields[1]+"\n")
	runTestCommand(t, repository, "git", "config", "gpg.format", "ssh")
	runTestCommand(t, repository, "git", "config", "user.signingkey", key)
	runTestCommand(t, repository, "git", "config", "gpg.ssh.allowedSignersFile", allowed)
	return allowed
}

func writeTransitionState(t *testing.T, moduleDir, generatedSignature string, guide, tamperGenerated bool) {
	t.Helper()
	observation, err := os.ReadFile(filepath.Join(moduleRoot(t), "observe", "telemetry-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeTransitionEvidence(t, moduleDir, generatedSignature, guide, tamperGenerated, compatibility.DevelopmentManifest().Digests.CLIJSON, observation)
}

func writeTransitionEvidence(t *testing.T, moduleDir, generatedSignature string, guide, tamperGenerated bool, cliDigest string, observation []byte) {
	t.Helper()
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), string(mustReadTestFile(t, "go.mod")))
	for _, relative := range []string{compatibility.DependencyLicenseAuthorityPath, compatibility.ProjectLicensePath, compatibility.ThirdPartyNoticesPath} {
		writeTestFile(t, filepath.Join(moduleDir, filepath.FromSlash(relative)), string(mustReadTestFile(t, relative)))
	}
	writeTestFile(t, filepath.Join(moduleDir, "internal", "compatibility", "testdata", "p7", "fixture.txt"), "retained p7 corpus\n")
	writeTestFile(t, filepath.Join(moduleDir, "internal", "compatibility", "testdata", "p7-event", "fixture.txt"), "retained p7 event corpus\n")
	public := transitionAPIInventory(t, "struct{Value string}")
	generated := transitionAPIInventory(t, generatedSignature)
	graphQL := transitionGraphQLInventory(t)
	value := compatibility.DevelopmentManifest()
	value.Digests.PublicGoAPI = compatibility.Digest(public)
	value.Digests.GeneratedGoABI = compatibility.Digest(generated)
	value.Digests.GraphQLABI = compatibility.Digest(graphQL)
	value.Digests.CLIJSON = cliDigest
	value.Digests.Observation = compatibility.Digest(observation)
	if !guide {
		value.MigrationGuide = nil
		value.RequiredActions = []string{"migrate.database", "regenerate.generated"}
	}
	manifestBytes, err := compatibility.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "compatibility", "manifest.json"), string(manifestBytes))
	writeTestFile(t, filepath.Join(moduleDir, "internal", "compatibility", "testdata", "public-go-api.json"), string(public))
	writeTestFile(t, filepath.Join(moduleDir, "internal", "compatibility", "testdata", "generated-go-abi.json"), string(generated))
	writeTestFile(t, filepath.Join(moduleDir, "internal", "compatibility", "testdata", "graphql-abi.json"), string(graphQL))
	writeTestFile(t, filepath.Join(moduleDir, "observe", "telemetry-manifest.json"), string(observation))
	if guide {
		encoded, err := os.ReadFile(filepath.Join(moduleRoot(t), filepath.FromSlash(compatibility.MigrationGuidePath)))
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(moduleDir, filepath.FromSlash(compatibility.MigrationGuidePath)), string(encoded))
	}
	if tamperGenerated {
		tampered := transitionAPIInventory(t, "struct{Value bool}")
		if compatibility.Digest(tampered) == value.Digests.GeneratedGoABI {
			t.Fatal("tampered generated corpus retained signed digest")
		}
		writeTestFile(t, filepath.Join(moduleDir, "internal", "compatibility", "testdata", "generated-go-abi.json"), string(tampered))
	}
}

func writeSyntheticTransitionGuide(t *testing.T, repository, moduleDir, previousCommit, currentVersion string) {
	t.Helper()
	paths := []string{"internal/compatibility/testdata/p7", "internal/compatibility/testdata/p7-event"}
	corpora := make([]compatibility.MigrationGuideCorpus, len(paths))
	for index, path := range paths {
		corpora[index] = compatibility.MigrationGuideCorpus{Path: path, GitTree: strings.TrimSpace(runTestCommandOutput(t, repository, "git", "rev-parse", previousCommit+":go/"+path))}
	}
	guide := compatibility.MigrationGuide{
		FormatVersion: 1,
		From:          compatibility.MigrationGuideEndpoint{Tag: "go/v0.0.2", Commit: previousCommit},
		ToVersion:     currentVersion,
		RequiredActions: []string{
			"migrate.database", "migration-guide.execute", "regenerate.generated",
		},
		Corpora: corpora,
		Evidence: []compatibility.MigrationGuideEvidence{{
			Identity: ModulePath + "/internal/release:TestP8SignedCompatibilityTransitionRequiresPreStableMinorGuideAndAncestry",
			Command:  "go test ./internal/release -run '^TestP8SignedCompatibilityTransitionRequiresPreStableMinorGuideAndAncestry$' -count=1",
		}},
	}
	encoded, err := compatibility.EncodeMigrationGuide(guide)
	if err != nil {
		t.Fatal(err)
	}
	const guidePath = "compatibility/migration-guide-synthetic.json"
	writeTestFile(t, filepath.Join(moduleDir, filepath.FromSlash(guidePath)), string(encoded))
	manifestBytes, err := os.ReadFile(filepath.Join(moduleDir, "compatibility", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := compatibility.Parse(manifestBytes, compatibility.Digest(manifestBytes))
	if err != nil {
		t.Fatal(err)
	}
	value.MigrationGuide = &compatibility.MigrationGuideAuthority{Path: guidePath, SHA256: compatibility.MigrationGuideDigest(encoded), FromTag: "go/v0.0.2", ToVersion: currentVersion}
	value.RequiredActions = append([]string(nil), guide.RequiredActions...)
	manifestBytes, err = compatibility.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "compatibility", "manifest.json"), string(manifestBytes))
}

func mutateTransitionGuideManifest(t *testing.T, moduleDir string, mutate func(*compatibility.Manifest)) {
	t.Helper()
	path := filepath.Join(moduleDir, "compatibility", "manifest.json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := compatibility.Parse(encoded, compatibility.Digest(encoded))
	if err != nil {
		t.Fatal(err)
	}
	mutate(&value)
	encoded, err = compatibility.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, string(encoded))
}

func mutateSyntheticGuide(t *testing.T, moduleDir string, mutate func(*compatibility.MigrationGuide)) {
	t.Helper()
	manifestPath := filepath.Join(moduleDir, "compatibility", "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := compatibility.Parse(manifestBytes, compatibility.Digest(manifestBytes))
	if err != nil || manifest.MigrationGuide == nil {
		t.Fatalf("parse synthetic guide manifest: %v", err)
	}
	guidePath := filepath.Join(moduleDir, filepath.FromSlash(manifest.MigrationGuide.Path))
	encoded, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatal(err)
	}
	guide, err := compatibility.ParseMigrationGuide(encoded, manifest.MigrationGuide.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&guide)
	encoded, err = compatibility.EncodeMigrationGuide(guide)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, guidePath, string(encoded))
	manifest.MigrationGuide.SHA256 = compatibility.MigrationGuideDigest(encoded)
	manifestBytes, err = compatibility.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manifestPath, string(manifestBytes))
}

func transitionAPIInventory(t *testing.T, signature string) []byte {
	t.Helper()
	encoded, err := compatibility.EncodeAPIInventory(compatibility.APIInventory{
		FormatVersion: compatibility.APIInventoryFormatVersion,
		Packages: []compatibility.APIPackage{{Path: "example.test/api", Declarations: []compatibility.APIDeclaration{{
			Name: "Value", Kind: "type", TypeKind: "struct", Signature: signature, Methods: []string{},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func transitionGraphQLInventory(t *testing.T) []byte {
	t.Helper()
	encoded, err := compatibility.EncodeGraphQLInventory(compatibility.GraphQLInventory{
		FormatVersion: compatibility.GraphQLInventoryFormatVersion,
		Roots:         []compatibility.GraphQLRoot{{Operation: "query", Type: "Query"}},
		Definitions: []compatibility.GraphQLDefinition{{
			Name: "Query", Kind: "OBJECT", Interfaces: []string{}, Types: []string{}, EnumValues: []string{}, Directives: []string{},
			Fields: []compatibility.GraphQLField{{Name: "value", Type: "String!", Arguments: []compatibility.GraphQLArgument{}, Directives: []string{}}},
		}},
		Directives: []compatibility.GraphQLDirective{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func mustReadTestFile(t *testing.T, relative string) []byte {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(moduleRoot(t), filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mustReadPath(t *testing.T, path string) []byte {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func errorCode(err error) ErrorCode {
	var failure *Error
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runTestCommand(t *testing.T, directory, executable string, arguments ...string) {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(arguments, " "), err, output)
	}
}

func runTestCommandOutput(t *testing.T, directory, executable string, arguments ...string) string {
	t.Helper()
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", executable, strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
