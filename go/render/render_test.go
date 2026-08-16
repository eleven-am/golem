package render

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexSource = `<html><head><title>Original</title><meta charset="utf-8"></head><body><div id="root"></div></body></html>`

func bundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                     {Data: []byte(indexSource)},
		"assets/app-a1b2c3d4e5.js":       {Data: []byte("console.log(1)")},
		"assets/style.css":               {Data: []byte("body{}")},
		"favicon.ico":                    {Data: []byte("icon")},
		".env":                           {Data: []byte("SECRET=1")},
		"sub/nested.txt":                 {Data: []byte("nested")},
		"manifest-0123456789abcdef.json": {Data: []byte("{}")},
	}
}

func newTestRenderer(t *testing.T, config Config) *Renderer {
	t.Helper()
	if config.Files == nil && config.Dir == "" {
		config.Files = bundle()
	}
	renderer, err := New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return renderer
}

func get(t *testing.T, renderer *Renderer, target string, headers map[string]string) *http.Response {
	t.Helper()
	return do(t, renderer, http.MethodGet, target, headers)
}

func do(t *testing.T, renderer *Renderer, method, target string, headers map[string]string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	renderer.Handler().ServeHTTP(recorder, request)
	return recorder.Result()
}

func body(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestAssetCacheControlFollowsHashHeuristic(t *testing.T) {
	renderer := newTestRenderer(t, Config{})
	for _, testCase := range []struct {
		target string
		cache  string
	}{
		{"/assets/app-a1b2c3d4e5.js", "public, max-age=31536000, immutable"},
		{"/manifest-0123456789abcdef.json", "public, max-age=31536000, immutable"},
		{"/assets/style.css", "no-cache"},
		{"/favicon.ico", "no-cache"},
	} {
		response := get(t, renderer, testCase.target, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", testCase.target, response.StatusCode)
		}
		if got := response.Header.Get("Cache-Control"); got != testCase.cache {
			t.Fatalf("%s cache-control=%q want %q", testCase.target, got, testCase.cache)
		}
	}
}

func TestShellResponseIsNoCache(t *testing.T) {
	renderer := newTestRenderer(t, Config{})
	response := get(t, renderer, "/deep/route", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache-control=%q", got)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("content-type=%q", got)
	}
}

func TestDirectoryRequestNeverListsRedirectsOrServes(t *testing.T) {
	renderer := newTestRenderer(t, Config{})
	for _, target := range []string{"/assets", "/assets/", "/sub"} {
		response := get(t, renderer, target, nil)
		if response.StatusCode == http.StatusMovedPermanently || response.StatusCode == http.StatusFound {
			t.Fatalf("%s redirected with %d", target, response.StatusCode)
		}
		if strings.Contains(body(t, response), "nested.txt") {
			t.Fatalf("%s listed the directory", target)
		}
	}
}

func TestDotfilePathIs404EvenWhenFileExists(t *testing.T) {
	renderer := newTestRenderer(t, Config{})
	response := get(t, renderer, "/.env", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", response.StatusCode)
	}
	if strings.Contains(body(t, response), "SECRET") {
		t.Fatal("dotfile contents were served")
	}
}

func TestIndexPathServesRenderedShellNotRawFile(t *testing.T) {
	renderer := newTestRenderer(t, Config{Defaults: Meta{Title: "Rendered"}})
	response := get(t, renderer, "/index.html", nil)
	content := body(t, response)
	if !strings.Contains(content, "<title>Rendered</title>") {
		t.Fatalf("index path did not render the shell: %s", content)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache-control=%q", got)
	}
}

func TestHeadServesShellHeadersWithoutBody(t *testing.T) {
	renderer := newTestRenderer(t, Config{Defaults: Meta{Title: "Head"}})
	head := do(t, renderer, http.MethodHead, "/deep/route", nil)
	if head.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", head.StatusCode)
	}
	if content := body(t, head); content != "" {
		t.Fatalf("HEAD returned a body: %q", content)
	}
	get := get(t, renderer, "/deep/route", nil)
	if head.Header.Get("Content-Type") != get.Header.Get("Content-Type") {
		t.Fatal("HEAD and GET disagree on content type")
	}
	if head.Header.Get("Content-Length") != get.Header.Get("Content-Length") {
		t.Fatalf("HEAD content-length=%q GET=%q",
			head.Header.Get("Content-Length"), get.Header.Get("Content-Length"))
	}
}

func TestRequestClassification(t *testing.T) {
	renderer := newTestRenderer(t, Config{
		Reserved: []string{"/api", "/graphql"},
		Routes: []Route{{
			Patterns: []string{"/post/{id}"},
			Resolve: func(context.Context, Link) (*Meta, error) {
				return &Meta{Title: "Routed"}, nil
			},
		}},
	})
	for _, testCase := range []struct {
		name    string
		method  string
		target  string
		headers map[string]string
		status  int
		marker  string
	}{
		{name: "reserved", method: "GET", target: "/api/users", status: http.StatusNotFound},
		{name: "reserved-exact", method: "GET", target: "/graphql", status: http.StatusNotFound},
		{name: "method", method: "POST", target: "/deep", status: http.StatusNotFound},
		{name: "dotfile", method: "GET", target: "/.env", status: http.StatusNotFound},
		{name: "static", method: "GET", target: "/assets/style.css", status: http.StatusOK},
		{name: "route", method: "GET", target: "/post/7", status: http.StatusOK, marker: "Routed"},
		{name: "missing-asset", method: "GET", target: "/assets/gone.js", status: http.StatusNotFound},
		{name: "subresource", method: "GET", target: "/deep",
			headers: map[string]string{"Sec-Fetch-Dest": "script"}, status: http.StatusNotFound},
		{name: "document-dest", method: "GET", target: "/deep.js",
			headers: map[string]string{"Sec-Fetch-Dest": "document"}, status: http.StatusOK},
		{name: "non-html-accept", method: "GET", target: "/deep",
			headers: map[string]string{"Accept": "application/json"}, status: http.StatusNotFound},
		{name: "shell", method: "GET", target: "/deep/route", status: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := do(t, renderer, testCase.method, testCase.target, testCase.headers)
			if response.StatusCode != testCase.status {
				t.Fatalf("status=%d want %d", response.StatusCode, testCase.status)
			}
			if testCase.marker != "" && !strings.Contains(body(t, response), testCase.marker) {
				t.Fatalf("response missing %q", testCase.marker)
			}
		})
	}
}

func TestStartupValidation(t *testing.T) {
	noHead := fstest.MapFS{"index.html": {Data: []byte("<html><body>x</body></html>")}}
	for _, testCase := range []struct {
		name   string
		config Config
		code   ErrorCode
	}{
		{name: "neither-source", config: Config{}, code: CodeConfigInvalid},
		{name: "both-sources", config: Config{Dir: "somewhere", Files: bundle()}, code: CodeConfigInvalid},
		{name: "missing-bundle", config: Config{Dir: "/nonexistent/bundle/path"}, code: CodeBundleMissing},
		{name: "missing-index", config: Config{Files: fstest.MapFS{}}, code: CodeBundleMissing},
		{name: "no-head", config: Config{Files: noHead}, code: CodeIndexInvalid},
		{name: "escaping-index", config: Config{Files: bundle(), Index: "../outside.html"}, code: CodeIndexInvalid},
		{name: "bad-base-url", config: Config{Files: bundle(), BaseURL: "not a url"}, code: CodeConfigInvalid},
		{name: "empty-reserved", config: Config{Files: bundle(), Reserved: []string{"  "}}, code: CodeConfigInvalid},
		{name: "no-patterns", config: Config{Files: bundle(),
			Routes: []Route{{Resolve: func(context.Context, Link) (*Meta, error) { return nil, nil }}}}, code: CodeRouteInvalid},
		{name: "nil-resolver", config: Config{Files: bundle(),
			Routes: []Route{{Patterns: []string{"/a"}}}}, code: CodeRouteInvalid},
		{name: "relative-pattern", config: Config{Files: bundle(),
			Routes: []Route{{Patterns: []string{"a"}, Resolve: func(context.Context, Link) (*Meta, error) { return nil, nil }}}}, code: CodeRouteInvalid},
		{name: "duplicate-pattern", config: Config{Files: bundle(),
			Routes: []Route{{Patterns: []string{"/a", "/a"}, Resolve: func(context.Context, Link) (*Meta, error) { return nil, nil }}}}, code: CodeRouteInvalid},
		{name: "reserved-pattern", config: Config{Files: bundle(),
			Routes: []Route{{Patterns: []string{"/api/thing"}, Resolve: func(context.Context, Link) (*Meta, error) { return nil, nil }}}}, code: CodeRouteInvalid},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := New(testCase.config)
			if err == nil {
				t.Fatal("configuration was accepted")
			}
			code, ok := CodeOf(err)
			if !ok || code != testCase.code {
				t.Fatalf("code=%q ok=%v want %q (%v)", code, ok, testCase.code, err)
			}
		})
	}
}

func TestMissingBundleNamesResolvedAbsolutePath(t *testing.T) {
	_, err := New(Config{Dir: "relative/missing/bundle"})
	if err == nil {
		t.Fatal("missing bundle was accepted")
	}
	if !strings.HasPrefix(strings.TrimPrefix(err.Error(), string(CodeBundleMissing)+": bundle directory "), "/") {
		t.Fatalf("message does not name an absolute path: %v", err)
	}
}

func TestRouteMetadataInjectsOpenGraphAndTwitterTags(t *testing.T) {
	renderer := newTestRenderer(t, Config{
		Routes: []Route{{
			Patterns: []string{"/post/{id}"},
			Resolve: func(_ context.Context, link Link) (*Meta, error) {
				return &Meta{
					Title:       "Post " + link.Values["id"],
					Description: "An excerpt",
					Image:       "/cover.png",
				}, nil
			},
		}},
	})
	content := body(t, get(t, renderer, "/post/42", nil))
	for _, expected := range []string{
		`<title>Post 42</title>`,
		`<meta name="description" content="An excerpt">`,
		`<meta property="og:type" content="website">`,
		`<meta property="og:title" content="Post 42">`,
		`<meta property="og:image" content="/cover.png">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta name="twitter:title" content="Post 42">`,
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("missing %s in:\n%s", expected, content)
		}
	}
}

func TestMetadataNamesAndValuesAreHTMLEscaped(t *testing.T) {
	renderer := newTestRenderer(t, Config{
		Routes: []Route{{
			Patterns: []string{"/x"},
			Resolve: func(context.Context, Link) (*Meta, error) {
				return &Meta{
					Title:       `</title><script>alert(1)</script>`,
					Description: `" onload="alert(2)`,
					Tags:        map[string]string{`evil"name`: `<img src=x onerror=alert(3)>`},
				}, nil
			},
		}},
	})
	content := body(t, get(t, renderer, "/x", nil))
	for _, forbidden := range []string{
		"<script>alert(1)</script>",
		`" onload="alert(2)`,
		"<img src=x onerror=alert(3)>",
		`evil"name`,
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("unescaped %q in:\n%s", forbidden, content)
		}
	}
	if !strings.Contains(content, "&lt;script&gt;") {
		t.Fatalf("title was not escaped:\n%s", content)
	}
}

func TestShellTitleReplacedOnlyWhenTitleRendered(t *testing.T) {
	withTitle := newTestRenderer(t, Config{Defaults: Meta{Title: "Replaced"}})
	content := body(t, get(t, withTitle, "/deep", nil))
	if strings.Contains(content, "<title>Original</title>") {
		t.Fatal("original title survived a rendered title")
	}
	if !strings.Contains(content, "<title>Replaced</title>") {
		t.Fatal("rendered title missing")
	}

	withoutTitle := newTestRenderer(t, Config{Defaults: Meta{Description: "Only a description"}})
	content = body(t, get(t, withoutTitle, "/deep", nil))
	if !strings.Contains(content, "<title>Original</title>") {
		t.Fatalf("original title was stripped without a replacement:\n%s", content)
	}
}

func TestOriginalBytesWhenNoDefaultsAndNoMetadata(t *testing.T) {
	renderer := newTestRenderer(t, Config{})
	content := body(t, get(t, renderer, "/deep/route", nil))
	if content != indexSource {
		t.Fatalf("shell was re-rendered:\n%s", content)
	}
}

func TestBaseURLExpansionIsOptOut(t *testing.T) {
	route := Route{
		Patterns: []string{"/p"},
		Resolve: func(context.Context, Link) (*Meta, error) {
			return &Meta{Title: "T", Image: "/cover.png", URL: "/p"}, nil
		},
	}
	relative := newTestRenderer(t, Config{Routes: []Route{route}})
	content := body(t, get(t, relative, "/p", nil))
	if !strings.Contains(content, `content="/cover.png"`) {
		t.Fatalf("relative image was expanded without BaseURL:\n%s", content)
	}

	absolute := newTestRenderer(t, Config{BaseURL: "https://example.test", Routes: []Route{route}})
	content = body(t, get(t, absolute, "/p", nil))
	if !strings.Contains(content, `content="https://example.test/cover.png"`) {
		t.Fatalf("BaseURL did not expand the image:\n%s", content)
	}
	if !strings.Contains(content, `content="https://example.test/p"`) {
		t.Fatalf("BaseURL did not expand the url:\n%s", content)
	}
}

func TestTagMergeOrderAndOmission(t *testing.T) {
	renderer := newTestRenderer(t, Config{
		Defaults: Meta{Title: "Default", Tags: map[string]string{"og:site_name": "Site"}},
		Routes: []Route{{
			Patterns: []string{"/p"},
			Resolve: func(context.Context, Link) (*Meta, error) {
				return &Meta{
					Title: "Route",
					Tags:  map[string]string{"og:type": "article"},
					Omit:  []string{"og:url"},
				}, nil
			},
		}},
	})
	content := body(t, get(t, renderer, "/p", nil))
	if !strings.Contains(content, `content="article"`) {
		t.Fatalf("Tags did not override the shorthand og:type:\n%s", content)
	}
	if !strings.Contains(content, `content="Site"`) {
		t.Fatalf("default tags were dropped:\n%s", content)
	}
	if strings.Contains(content, `property="og:url"`) {
		t.Fatalf("Omit did not delete og:url:\n%s", content)
	}
	if !strings.Contains(content, "<title>Route</title>") {
		t.Fatal("route title did not override the default")
	}
}

func TestMultiplePatternsShareOneResolver(t *testing.T) {
	calls := 0
	renderer := newTestRenderer(t, Config{
		Routes: []Route{{
			Patterns: []string{"/movie/{name}", "/film/{name}"},
			Resolve: func(_ context.Context, link Link) (*Meta, error) {
				calls++
				return &Meta{Title: link.Values["name"]}, nil
			},
		}},
	})
	for _, target := range []string{"/movie/dune", "/film/dune"} {
		if content := body(t, get(t, renderer, target, nil)); !strings.Contains(content, "<title>dune</title>") {
			t.Fatalf("%s did not resolve through the shared resolver:\n%s", target, content)
		}
	}
	if calls != 2 {
		t.Fatalf("resolver calls=%d want 2", calls)
	}
}

func TestResolverFailureRendersDefaults(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		resolver Resolver
		reported bool
	}{
		{name: "error", reported: true, resolver: func(context.Context, Link) (*Meta, error) {
			return nil, errors.New("upstream is down")
		}},
		{name: "nil", reported: false, resolver: func(context.Context, Link) (*Meta, error) {
			return nil, nil
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reported := 0
			renderer := newTestRenderer(t, Config{
				Defaults:            Meta{Title: "Fallback"},
				Routes:              []Route{{Patterns: []string{"/p"}, Resolve: testCase.resolver}},
				ReportResolverError: func(context.Context, error) { reported++ },
			})
			response := get(t, renderer, "/p", nil)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d want 200", response.StatusCode)
			}
			if content := body(t, response); !strings.Contains(content, "<title>Fallback</title>") {
				t.Fatalf("defaults were not rendered:\n%s", content)
			}
			if testCase.reported && reported == 0 {
				t.Fatal("error was not reported")
			}
			if !testCase.reported && reported != 0 {
				t.Fatal("a nil result was reported as an error")
			}
		})
	}
}

func TestResolverPanicRendersDefaultsAndReports(t *testing.T) {
	reported := 0
	renderer := newTestRenderer(t, Config{
		Defaults: Meta{Title: "Fallback"},
		Routes: []Route{{
			Patterns: []string{"/p"},
			Resolve: func(context.Context, Link) (*Meta, error) {
				panic("resolver exploded")
			},
		}},
		ReportResolverError: func(context.Context, error) { reported++ },
	})
	response := get(t, renderer, "/p", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", response.StatusCode)
	}
	if content := body(t, response); !strings.Contains(content, "<title>Fallback</title>") {
		t.Fatalf("panic did not render defaults:\n%s", content)
	}
	if reported != 1 {
		t.Fatalf("panic reports=%d want 1", reported)
	}
}
