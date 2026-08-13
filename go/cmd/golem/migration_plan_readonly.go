package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type migrationPlanTreeEntry struct {
	path    string
	mode    fs.FileMode
	size    int64
	digest  [sha256.Size]byte
	symlink string
}

type migrationPlanTreeSnapshot struct {
	entries []migrationPlanTreeEntry
}

// snapshotMigrationPlanTree records every node without following symlinks.
// File contents, permissions and type bits, directory entries, and symlink
// targets are therefore all part of the read-only command invariant.
func snapshotMigrationPlanTree(root string) (migrationPlanTreeSnapshot, error) {
	var result migrationPlanTreeSnapshot
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		value := migrationPlanTreeEntry{path: filepath.ToSlash(relative), mode: info.Mode(), size: info.Size()}
		switch {
		case info.Mode().IsRegular():
			content, readErr := os.ReadFile(filename)
			if readErr != nil {
				return readErr
			}
			value.digest = sha256.Sum256(content)
		case info.Mode()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(filename)
			if readErr != nil {
				return readErr
			}
			value.symlink = target
		case info.IsDir():
		default:
			return fmt.Errorf("module tree contains unsupported filesystem node")
		}
		result.entries = append(result.entries, value)
		return nil
	})
	if err != nil {
		return migrationPlanTreeSnapshot{}, errors.New("module tree could not be snapshotted")
	}
	sort.Slice(result.entries, func(i, j int) bool { return result.entries[i].path < result.entries[j].path })
	return result, nil
}

func verifyMigrationPlanTree(root string, before migrationPlanTreeSnapshot) error {
	after, err := snapshotMigrationPlanTree(root)
	if err != nil {
		return err
	}
	if len(before.entries) != len(after.entries) {
		return errors.New("migration plan changed the module tree")
	}
	for index := range before.entries {
		left, right := before.entries[index], after.entries[index]
		if left.path != right.path || left.mode != right.mode || left.size != right.size || left.symlink != right.symlink || !bytes.Equal(left.digest[:], right.digest[:]) {
			return errors.New("migration plan changed the module tree")
		}
	}
	return nil
}
