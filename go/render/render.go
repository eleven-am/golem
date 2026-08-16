// Package render hosts a compiled single-page application and injects
// per-route Open Graph and Twitter metadata so links unfurl.
//
// The package is HTTP transport. It performs no data access: an application
// supplies a Resolver for each route, and how that function obtains its facts
// is invisible here. A link preview is requested by a crawler holding nothing
// but a URL, so a resolver should return only what any unauthenticated visitor
// may see.
package render

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Meta is the metadata rendered into the shell for one response. The empty
// string means unset. Tags override the tags generated from the shorthand
// fields, and Omit deletes generated tags by name.
type Meta struct {
	Title       string
	Description string
	Image       string
	URL         string
	Tags        map[string]string
	Omit        []string
}

// Link is the request material a metadata resolver may read. Values holds the
// pattern wildcards resolved by http.ServeMux, so a route registered as
// "/f/{token}" yields Values["token"]. No header is carried: none is
// meaningful when the caller is a crawler.
type Link struct {
	Path   string
	Values map[string]string
	Query  url.Values
	URL    string
}

// Resolver produces the metadata for one matched route. Returning a nil Meta,
// returning an error, or panicking renders the configured defaults.
type Resolver func(ctx context.Context, link Link) (*Meta, error)

// Route binds one resolver to one or more http.ServeMux path patterns.
type Route struct {
	Patterns []string
	Resolve  Resolver
}

// Config declares a renderer. Exactly one of Dir and Files must be set.
type Config struct {
	Dir                 string
	Files               fs.FS
	Index               string
	Reserved            []string
	Defaults            Meta
	BaseURL             string
	Routes              []Route
	ReportResolverError func(context.Context, error)
}

// ErrorCode is the closed set of render failure classifications.
type ErrorCode string

const (
	CodeConfigInvalid ErrorCode = "RENDER_CONFIG_INVALID"
	CodeBundleMissing ErrorCode = "RENDER_BUNDLE_MISSING"
	CodeIndexInvalid  ErrorCode = "RENDER_INDEX_INVALID"
	CodeRouteInvalid  ErrorCode = "RENDER_ROUTE_INVALID"
)

type renderError struct {
	code   ErrorCode
	detail string
}

func (failure renderError) Error() string {
	return string(failure.code) + ": " + failure.detail
}

func fail(code ErrorCode, format string, arguments ...any) error {
	return renderError{code: code, detail: fmt.Sprintf(format, arguments...)}
}

// CodeOf reports the classification of a render failure. It recognises
// wrapped errors and never classifies from message text.
func CodeOf(err error) (ErrorCode, bool) {
	var failure renderError
	if errors.As(err, &failure) {
		return failure.code, true
	}
	return "", false
}

// Renderer serves a bundle and its rendered shell.
type Renderer struct {
	files    fs.FS
	index    string
	reserved []string
	defaults Meta
	base     *url.URL
	shell    shell
	routes   *http.ServeMux
	report   func(context.Context, error)
}

// New validates the configuration, reads and parses the index once, and
// returns a renderer. Every failure is reported here rather than deferred to
// the first request.
func New(config Config) (*Renderer, error) {
	if (config.Dir == "") == (config.Files == nil) {
		return nil, fail(CodeConfigInvalid, "exactly one of Dir or Files must be set")
	}
	index := config.Index
	if index == "" {
		index = "index.html"
	}
	if path.IsAbs(index) || !fs.ValidPath(path.Clean(index)) || strings.HasPrefix(path.Clean(index), "..") {
		return nil, fail(CodeIndexInvalid, "index %q escapes the bundle", config.Index)
	}
	index = path.Clean(index)

	files := config.Files
	location := index
	if config.Dir != "" {
		absolute, err := filepath.Abs(config.Dir)
		if err != nil {
			return nil, fail(CodeConfigInvalid, "bundle directory %q could not be resolved", config.Dir)
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.IsDir() {
			return nil, fail(CodeBundleMissing, "bundle directory %s is missing", absolute)
		}
		files = os.DirFS(absolute)
		location = filepath.Join(absolute, filepath.FromSlash(index))
	}

	info, err := fs.Stat(files, index)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fail(CodeBundleMissing, "index %s is missing", location)
	}
	source, err := fs.ReadFile(files, index)
	if err != nil {
		return nil, fail(CodeBundleMissing, "index %s could not be read", location)
	}
	parsed, err := parseShell(source)
	if err != nil {
		return nil, err
	}

	var base *url.URL
	if config.BaseURL != "" {
		parsedBase, err := url.Parse(config.BaseURL)
		if err != nil || !parsedBase.IsAbs() {
			return nil, fail(CodeConfigInvalid, "BaseURL %q is not an absolute URL", config.BaseURL)
		}
		base = parsedBase
	}

	reserved, err := normalizeReserved(config.Reserved)
	if err != nil {
		return nil, err
	}

	renderer := &Renderer{
		files:    files,
		index:    index,
		reserved: reserved,
		defaults: config.Defaults,
		base:     base,
		shell:    parsed,
		routes:   http.NewServeMux(),
		report:   config.ReportResolverError,
	}
	if err := renderer.registerRoutes(config.Routes); err != nil {
		return nil, err
	}
	return renderer, nil
}

func normalizeReserved(entries []string) ([]string, error) {
	if entries == nil {
		return []string{"/api", "/graphql"}, nil
	}
	normalized := make([]string, 0, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimRight(strings.TrimSpace(entry), "/")
		if trimmed == "" {
			return nil, fail(CodeConfigInvalid, "reserved entry %q is empty", entry)
		}
		if !strings.HasPrefix(trimmed, "/") {
			trimmed = "/" + trimmed
		}
		normalized = append(normalized, trimmed)
	}
	return normalized, nil
}

func (renderer *Renderer) registerRoutes(routes []Route) error {
	seen := map[string]bool{}
	for _, route := range routes {
		if len(route.Patterns) == 0 {
			return fail(CodeRouteInvalid, "route declares no patterns")
		}
		if route.Resolve == nil {
			return fail(CodeRouteInvalid, "route %q has a nil resolver", route.Patterns[0])
		}
		for _, pattern := range route.Patterns {
			if !strings.HasPrefix(pattern, "/") {
				return fail(CodeRouteInvalid, "pattern %q does not start with /", pattern)
			}
			if seen[pattern] {
				return fail(CodeRouteInvalid, "pattern %q is declared twice", pattern)
			}
			if renderer.isReserved(routePath(pattern)) {
				return fail(CodeRouteInvalid, "pattern %q is under a reserved prefix", pattern)
			}
			if err := renderer.registerPattern(pattern, route.Resolve); err != nil {
				return err
			}
			seen[pattern] = true
		}
	}
	return nil
}

func (renderer *Renderer) registerPattern(pattern string, resolver Resolver) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fail(CodeRouteInvalid, "pattern %q is not a valid route pattern", pattern)
		}
	}()
	names := wildcards(pattern)
	renderer.routes.HandleFunc(pattern, func(writer http.ResponseWriter, request *http.Request) {
		values := make(map[string]string, len(names))
		for _, name := range names {
			values[name] = request.PathValue(name)
		}
		link := Link{
			Path:   request.URL.Path,
			Values: values,
			Query:  request.URL.Query(),
			URL:    renderer.absolute(request.URL.RequestURI()),
		}
		renderer.serveShell(writer, request, renderer.resolve(request.Context(), resolver, link))
	})
	return nil
}

func routePath(pattern string) string {
	if index := strings.IndexAny(pattern, "{"); index >= 0 {
		return pattern[:index]
	}
	return pattern
}

func (renderer *Renderer) isReserved(requestPath string) bool {
	clean := strings.TrimRight(requestPath, "/")
	if clean == "" {
		clean = "/"
	}
	for _, entry := range renderer.reserved {
		if clean == entry || strings.HasPrefix(clean, entry+"/") {
			return true
		}
	}
	return false
}

// Handler returns the http.Handler that serves the bundle and its shell.
func (renderer *Renderer) Handler() http.Handler {
	return http.HandlerFunc(renderer.serve)
}

func wildcards(pattern string) []string {
	names := []string{}
	remainder := pattern
	for {
		open := strings.Index(remainder, "{")
		if open < 0 {
			return names
		}
		closing := strings.Index(remainder[open:], "}")
		if closing < 0 {
			return names
		}
		names = append(names, strings.TrimSuffix(remainder[open+1:open+closing], "..."))
		remainder = remainder[open+closing+1:]
	}
}
