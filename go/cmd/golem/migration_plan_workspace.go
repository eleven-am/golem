package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type migrationPlanBuildWorkspace struct {
	directory string
	remove    func(string) error
}

var makeMigrationPlanBuildWorkspace = newMigrationPlanBuildWorkspace

func newMigrationPlanBuildWorkspace(moduleDir string) (migrationPlanBuildWorkspace, error) {
	return newMigrationPlanBuildWorkspaceAt(moduleDir, os.TempDir(), os.RemoveAll)
}

func newMigrationPlanBuildWorkspaceAt(moduleDir, temporaryRoot string, remove func(string) error) (migrationPlanBuildWorkspace, error) {
	module, err := resolvedMigrationPlanPath(moduleDir)
	if err != nil {
		return migrationPlanBuildWorkspace{}, errors.New("read-only prospective build workspace is unavailable")
	}
	root, err := resolvedMigrationPlanPath(temporaryRoot)
	if err != nil || migrationPlanPathWithin(module, root) {
		return migrationPlanBuildWorkspace{}, errors.New("read-only prospective build workspace must be outside the module")
	}
	directory, err := os.MkdirTemp(root, "golem-migration-plan-build-")
	if err != nil {
		return migrationPlanBuildWorkspace{}, errors.New("read-only prospective build workspace is unavailable")
	}
	resolved, err := resolvedMigrationPlanPath(directory)
	if err != nil || migrationPlanPathWithin(module, resolved) {
		_ = os.RemoveAll(directory)
		return migrationPlanBuildWorkspace{}, errors.New("read-only prospective build workspace must be outside the module")
	}
	return migrationPlanBuildWorkspace{directory: resolved, remove: remove}, nil
}

func (workspace migrationPlanBuildWorkspace) cleanup() error {
	if workspace.directory == "" || workspace.remove == nil {
		return errors.New("read-only prospective build workspace cleanup is unavailable")
	}
	if err := workspace.remove(workspace.directory); err != nil {
		return errors.New("read-only prospective build workspace cleanup failed")
	}
	if _, err := os.Lstat(workspace.directory); !errors.Is(err, os.ErrNotExist) {
		return errors.New("read-only prospective build workspace cleanup failed")
	}
	return nil
}

func resolvedMigrationPlanPath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func migrationPlanPathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
