// Package gomodfile resolves the module configuration under which Golem
// type-checks a consumer package together with generated source that may
// import packages the consumer's go.mod does not yet require.
package gomodfile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// IsolatedBuildFlags is BuildFlags with its alternate modfile in a private
// temporary directory, so resolution never writes inside the consumer module.
func IsolatedBuildFlags(ctx context.Context, moduleDir string, environment []string) ([]string, func(), error) {
	directory, err := os.MkdirTemp("", "golem-typecheck-")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create type-check module directory: %w", err)
	}
	flags, cleanup, err := BuildFlags(ctx, moduleDir, environment, directory)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, func() {}, err
	}
	return flags, func() {
		cleanup()
		_ = os.RemoveAll(directory)
	}, nil
}

// BuildFlags returns the go command flags that load the consumer module
// with every dependency its generated source needs, without rewriting the
// consumer's go.mod or go.sum. Inside an active workspace it is readonly
// resolution; otherwise an alternate modfile copied into modfileDir, or into
// the module when modfileDir is empty. The returned function removes it.
func BuildFlags(ctx context.Context, moduleDir string, environment []string, modfileDir string) ([]string, func(), error) {
	workspace, err := ActiveWorkspace(ctx, moduleDir, environment)
	if err != nil {
		return nil, func() {}, err
	}
	if workspace {
		return []string{"-mod=readonly"}, func() {}, nil
	}
	alternate, cleanup, err := modfileIn(moduleDir, modfileDir)
	if err != nil {
		return nil, func() {}, err
	}
	return []string{"-mod=mod", "-modfile=" + alternate}, cleanup, nil
}

// ActiveWorkspace reports whether the go command runs the module in
// workspace mode.
func ActiveWorkspace(ctx context.Context, moduleDir string, environment []string) (bool, error) {
	value, err := ActiveWorkspacePath(ctx, moduleDir, environment)
	return value != "" && value != "off" && value != os.DevNull, err
}

// ActiveWorkspacePath returns the go.work file the go command uses for the
// module, or its "off" or empty value.
func ActiveWorkspacePath(ctx context.Context, moduleDir string, environment []string) (string, error) {
	command := exec.CommandContext(ctx, "go", "env", "GOWORK")
	command.Dir = moduleDir
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve prospective Go workspace: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func modfileIn(moduleDir, ownedDir string) (string, func(), error) {
	content, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return "", func() {}, fmt.Errorf("read module file for prospective compilation: %w", err)
	}
	if ownedDir != "" {
		parsed, parseErr := modfile.Parse("go.mod", content, nil)
		if parseErr != nil {
			return "", func() {}, fmt.Errorf("parse module file for prospective compilation: %w", parseErr)
		}
		for _, replacement := range append([]*modfile.Replace(nil), parsed.Replace...) {
			if replacement.New.Version != "" || filepath.IsAbs(replacement.New.Path) {
				continue
			}
			absolute := filepath.Clean(filepath.Join(moduleDir, filepath.FromSlash(replacement.New.Path)))
			if replaceErr := parsed.AddReplace(replacement.Old.Path, replacement.Old.Version, filepath.ToSlash(absolute), ""); replaceErr != nil {
				return "", func() {}, fmt.Errorf("normalize module replacement for prospective compilation: %w", replaceErr)
			}
		}
		content, err = parsed.Format()
		if err != nil {
			return "", func() {}, fmt.Errorf("format module file for prospective compilation: %w", err)
		}
	}
	temporaryRoot := moduleDir
	pattern := ".golem-prospective-*.mod"
	if ownedDir != "" {
		temporaryRoot = ownedDir
		pattern = "golem-prospective-*.mod"
	}
	file, err := os.CreateTemp(temporaryRoot, pattern)
	if err != nil {
		return "", func() {}, fmt.Errorf("create prospective module file: %w", err)
	}
	name := file.Name()
	cleanup := func() {
		_ = os.Remove(name)
		_ = os.Remove(strings.TrimSuffix(name, ".mod") + ".sum")
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write prospective module file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close prospective module file: %w", err)
	}
	if sum, readErr := os.ReadFile(filepath.Join(moduleDir, "go.sum")); readErr == nil {
		if writeErr := os.WriteFile(strings.TrimSuffix(name, ".mod")+".sum", sum, 0o600); writeErr != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("write prospective sum file: %w", writeErr)
		}
	} else if !os.IsNotExist(readErr) {
		cleanup()
		return "", func() {}, fmt.Errorf("read module sums for prospective compilation: %w", readErr)
	}
	return name, cleanup, nil
}
