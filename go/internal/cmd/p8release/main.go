package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	releasetool "github.com/eleven-am/golem/go/internal/release"
)

type output struct {
	FormatVersion  int                   `json:"formatVersion"`
	Mode           string                `json:"mode"`
	Status         string                `json:"status"`
	Code           releasetool.ErrorCode `json:"code,omitempty"`
	Module         string                `json:"module,omitempty"`
	Version        string                `json:"version,omitempty"`
	Tag            string                `json:"tag,omitempty"`
	Commit         string                `json:"commit,omitempty"`
	ManifestSHA256 string                `json:"manifestSHA256,omitempty"`
	SourceSHA256   string                `json:"sourceTreeSHA256,omitempty"`
	SignersSHA256  string                `json:"allowedSignersSHA256,omitempty"`
	InventorySHA   string                `json:"inventorySHA256,omitempty"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(arguments []string, stdout io.Writer) int {
	flags := flag.NewFlagSet("p8release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	mode := flags.String("mode", "verify", "verify or build")
	tag := flags.String("tag", "", "exact signed nested-module tag go/vX.Y.Z")
	module := flags.String("module", ".", "Go module directory")
	allowedSigners := flags.String("allowed-signers", os.Getenv("GOLEM_RELEASE_ALLOWED_SIGNERS"), "pinned SSH allowed-signers trust file")
	allowedSignersSHA256 := flags.String("allowed-signers-sha256", os.Getenv("GOLEM_RELEASE_ALLOWED_SIGNERS_SHA256"), "separately protected SHA-256 of allowed-signers")
	outputDir := flags.String("output", "", "new artifact output directory")
	proxy := flags.String("proxy", os.Getenv("GOPROXY"), "candidate module proxy")
	timeout := flags.Duration("timeout", 30*time.Minute, "bounded verification timeout")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || !closedMode(*mode) || *tag == "" || *module == "" || *allowedSigners == "" || *allowedSignersSHA256 == "" || *timeout <= 0 {
		return writeFailure(stdout, "", releasetool.CodeInvalidCandidate)
	}
	absolute, err := filepath.Abs(*module)
	if err != nil {
		return writeFailure(stdout, *mode, releasetool.CodeInvalidCandidate)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	candidate, err := releasetool.InspectCandidateWithConfig(ctx, releasetool.InspectConfig{ModuleDir: absolute, Tag: *tag, AllowedSignersFile: *allowedSigners, AllowedSignersSHA256: *allowedSignersSHA256})
	if err != nil {
		return writeFailure(stdout, *mode, codeOf(err))
	}
	inventorySHA := ""
	switch *mode {
	case "verify":
		if *proxy == "" {
			return writeFailure(stdout, *mode, releasetool.CodeConsumer)
		}
		if err := releasetool.VerifyCleanConsumer(ctx, releasetool.ConsumerConfig{Version: candidate.Version, Proxy: *proxy}); err != nil {
			return writeFailure(stdout, *mode, codeOf(err))
		}
	case "build":
		if *outputDir == "" {
			return writeFailure(stdout, *mode, releasetool.CodeInvalidCandidate)
		}
		if _, err := releasetool.Build(ctx, releasetool.BuildConfig{ModuleDir: absolute, OutputDir: *outputDir, Candidate: candidate}); err != nil {
			return writeFailure(stdout, *mode, codeOf(err))
		}
		inventorySHA, err = fileSHA256(filepath.Join(*outputDir, "release-inventory.json"))
	}
	if err != nil {
		return writeFailure(stdout, *mode, codeOf(err))
	}
	return writeOutput(stdout, output{
		FormatVersion: releasetool.FormatVersion, Mode: *mode, Status: "PASS", Module: candidate.Module, Version: candidate.Version, Tag: candidate.Tag, Commit: candidate.Commit,
		ManifestSHA256: digest(candidate.Manifest), SourceSHA256: candidate.SourceTreeSHA256, SignersSHA256: candidate.SignersSHA256, InventorySHA: inventorySHA,
	}, 0)
}

func closedMode(value string) bool {
	return value == "verify" || value == "build"
}

func codeOf(err error) releasetool.ErrorCode {
	var closed *releasetool.Error
	if errors.As(err, &closed) {
		return closed.Code
	}
	return releasetool.CodeBuild
}

func writeFailure(stdout io.Writer, mode string, code releasetool.ErrorCode) int {
	return writeOutput(stdout, output{FormatVersion: releasetool.FormatVersion, Mode: mode, Status: "FAIL", Code: code}, 1)
}

func writeOutput(stdout io.Writer, value output, exit int) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		return 2
	}
	return exit
}

func fileSHA256(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return digest(content), nil
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
