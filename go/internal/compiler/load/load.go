// Package load performs the syntax-only package load used by the first compiler
// pass. It deliberately does not type-check: generated handles do not exist yet.
package load

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Config selects exactly one package.
type Config struct {
	Dir     string
	Pattern string
	Env     []string
}

// Package is a deterministic syntax-only view of an application package.
type Package struct {
	ImportPath string
	Name       string
	Dir        string
	ModulePath string
	ModuleDir  string
	FileSet    *token.FileSet
	Files      []File
}

// File contains one non-generated, non-test Go source file.
type File struct {
	AbsolutePath string
	RelativePath string
	AST          *ast.File
}

// Error is a deterministic package-load or parse diagnostic. Paths are module
// relative whenever the go command supplied module metadata.
type Error struct {
	Code         string
	RelativeFile string
	Line         int
	Column       int
	Text         string
}

func (e Error) Error() string { return e.Text }

type listedPackage struct {
	ImportPath string
	Name       string
	Dir        string
	GoFiles    []string
	Module     *struct {
		Path string
		Dir  string
	}
	Error *struct {
		Err string
	}
}

// PackageSyntax loads and parses exactly one package selected by Pattern.
func PackageSyntax(ctx context.Context, config Config) (*Package, []Error) {
	pattern := config.Pattern
	if pattern == "" {
		pattern = "."
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-json", pattern)
	cmd.Dir = config.Dir
	if len(config.Env) != 0 {
		cmd.Env = append(os.Environ(), config.Env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, []Error{{Code: "P1_LOAD_GO_LIST", Text: message}}
	}

	decoder := json.NewDecoder(&stdout)
	var listed []listedPackage
	for decoder.More() {
		var item listedPackage
		if err := decoder.Decode(&item); err != nil {
			return nil, []Error{{Code: "P1_LOAD_GO_LIST_JSON", Text: fmt.Sprintf("decode go list output: %v", err)}}
		}
		listed = append(listed, item)
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].ImportPath < listed[j].ImportPath })
	if len(listed) != 1 {
		paths := make([]string, len(listed))
		for i := range listed {
			paths[i] = listed[i].ImportPath
		}
		return nil, []Error{{
			Code: "P1_LOAD_PACKAGE_COUNT",
			Text: fmt.Sprintf("schema pattern %q selected %d packages (%s); expected exactly one",
				pattern, len(listed), strings.Join(paths, ", ")),
		}}
	}

	item := listed[0]
	if item.Dir == "" || item.Name == "" {
		message := "selected package has no Go source"
		if item.Error != nil && item.Error.Err != "" {
			message = item.Error.Err
		}
		return nil, []Error{{Code: "P1_LOAD_PACKAGE", Text: message}}
	}

	result := &Package{
		ImportPath: item.ImportPath,
		Name:       item.Name,
		Dir:        item.Dir,
		FileSet:    token.NewFileSet(),
	}
	if item.Module != nil {
		result.ModulePath = item.Module.Path
		result.ModuleDir = item.Module.Dir
	}

	files := append([]string(nil), item.GoFiles...)
	sort.Strings(files)
	var errs []Error
	for _, name := range files {
		absolute := filepath.Join(item.Dir, name)
		contents, err := os.ReadFile(absolute)
		if err != nil {
			errs = append(errs, Error{Code: "P1_LOAD_READ", RelativeFile: result.relativePath(absolute), Text: err.Error()})
			continue
		}
		if generated(contents) {
			continue
		}
		parsed, err := parser.ParseFile(result.FileSet, absolute, contents, parser.ParseComments|parser.AllErrors)
		if err != nil {
			if list, ok := err.(scanner.ErrorList); ok {
				for _, parseErr := range list {
					errs = append(errs, Error{
						Code:         "P1_LOAD_PARSE",
						RelativeFile: result.relativePath(parseErr.Pos.Filename),
						Line:         parseErr.Pos.Line,
						Column:       parseErr.Pos.Column,
						Text:         parseErr.Msg,
					})
				}
			} else {
				errs = append(errs, Error{Code: "P1_LOAD_PARSE", RelativeFile: result.relativePath(absolute), Text: err.Error()})
			}
			continue
		}
		result.Files = append(result.Files, File{AbsolutePath: absolute, RelativePath: result.relativePath(absolute), AST: parsed})
	}
	sortErrors(errs)
	if len(errs) != 0 {
		return nil, errs
	}
	return result, nil
}

// Position returns a module-relative source coordinate.
func (p *Package) Position(pos token.Pos) (relativeFile string, line, column int) {
	position := p.FileSet.Position(pos)
	return p.relativePath(position.Filename), position.Line, position.Column
}

func (p *Package) relativePath(path string) string {
	base := p.ModuleDir
	if base == "" {
		base = p.Dir
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.Base(path)
	}
	return filepath.ToSlash(relative)
}

func generated(contents []byte) bool {
	lines := bytes.SplitN(contents, []byte("\n"), 11)
	for _, line := range lines {
		text := string(bytes.TrimSpace(line))
		if strings.HasPrefix(text, "// Code generated ") && strings.HasSuffix(text, " DO NOT EDIT.") {
			return true
		}
	}
	return false
}

func sortErrors(errs []Error) {
	sort.SliceStable(errs, func(i, j int) bool {
		left, right := errs[i], errs[j]
		if left.RelativeFile != right.RelativeFile {
			return left.RelativeFile < right.RelativeFile
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		if left.Column != right.Column {
			return left.Column < right.Column
		}
		return left.Code < right.Code
	})
}
