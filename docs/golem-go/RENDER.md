# Serving a single-page application

A single-page application returns the same HTML shell for every route, so a
crawler asking for `/n/42` sees the shell's metadata rather than the note's.
`render` serves the shell and rewrites its metadata per route, server-side,
before the bytes leave.

The program on this page is executed by `TestRenderApplicationRuns`.

## A renderer

```go
renderer, err := render.New(render.Config{
	Dir:     "public",
	Index:   "index.html",
	BaseURL: "https://notes.example",
	Defaults: render.Meta{
		Title:       "Notes",
		Description: "A place for notes",
	},
	Routes: []render.Route{{
		Patterns: []string{"/n/{id}"},
		Resolve: func(ctx context.Context, link render.Link) (*render.Meta, error) {
			return &render.Meta{Title: "Note " + link.Values["id"]}, nil
		},
	}},
})

http.ListenAndServe(":8080", renderer.Handler())
```

| Field | Meaning |
|---|---|
| `Dir` / `Files` | where the built assets live; set exactly one |
| `Index` | the shell, relative to the bundle; `index.html` when unset |
| `Defaults` | metadata every response starts from; a resolver's non-empty fields override it |
| `BaseURL` | absolute origin that `og:url` and image URLs are resolved against |
| `Reserved` | prefixes answered with 404 instead of the shell; `/api` and `/graphql` when nil |
| `Routes` | patterns bound to resolvers |
| `ReportResolverError` | where resolver errors and panics are reported |

Patterns are `http.ServeMux` patterns, so `/n/{id}` puts `id` in
`link.Values`.

## Resolvers fail safe

A resolver returns metadata for one matched route:

```go
func(ctx context.Context, link render.Link) (*render.Meta, error)
```

Returning `nil`, returning an error, **or panicking** renders the configured
defaults. A resolver is code that runs on an unauthenticated request from a
crawler; a bug in it degrades the preview rather than the page. Errors reach
`ReportResolverError` so a silent degradation is still observable; a panic
arrives there as an error carrying the recovered value.

`Link` carries the path, the pattern wildcards, the query and the absolute
URL — and deliberately no headers. Nothing in a crawler's headers is worth
trusting, and a resolver that cannot read them cannot accidentally
authenticate one.

## What it serves

```
/n/42      title="Note 42"  cache="no-cache"
/n/missing title="Notes"    cache="no-cache"
```

`/n/42` gets its resolver's metadata. `/n/missing` returns nil, so the
defaults are used — the page still renders.

Only `GET` and `HEAD` are served. A path that names a file in the bundle
gets that file. Anything else that looks like an asset — by extension, under
`/assets/`, or by a subresource `Sec-Fetch-Dest` — or whose `Accept` excludes
HTML gets a 404 rather than the shell, so a missing script is not answered
with a page.

## Caching

Every file and shell response carries `Cache-Control: no-cache`; a 404
carries no cache directive. Assets are not fingerprinted
by the renderer, and it cannot tell a content-hashed filename from one that
merely looks like it, so it does not guess. A wrong guess serves a stale asset
for as long as the directive says — put a CDN in front and set the policy where
the filenames are known.

## Reserved prefixes

```go
Reserved: []string{"/api", "/graphql"}
```

The renderer answers a reserved path with 404 itself, so a mistyped API path
that reaches it gets a 404 rather than an HTML page a client will try to parse
as JSON. `/api` and `/graphql` are reserved when `Reserved` is nil; an empty
slice reserves nothing. A route pattern under a reserved prefix is refused by
`New` with `RENDER_ROUTE_INVALID`.

## It is independent

`render` needs no schema, no database and no generated application. It is an
`http.Handler`. Mount it beside your API, or run it alone.

## The whole program

```go
// cmd/site/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/eleven-am/golem/go/render"
)

func main() {
	renderer, err := render.New(render.Config{
		Dir:     "public",
		Index:   "index.html",
		BaseURL: "https://notes.example",
		Defaults: render.Meta{
			Title:       "Notes",
			Description: "A place for notes",
		},
		Routes: []render.Route{{
			Patterns: []string{"/n/{id}"},
			Resolve: func(_ context.Context, link render.Link) (*render.Meta, error) {
				id := link.Values["id"]
				if id == "missing" {
					return nil, nil
				}
				return &render.Meta{
					Title:       "Note " + id,
					Description: "shared note " + id,
				}, nil
			},
		}},
	})
	if err != nil {
		log.Fatal(err)
	}

	server := httptest.NewServer(renderer.Handler())
	defer server.Close()

	for _, path := range []string{"/n/42", "/n/missing"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			log.Fatal(err)
		}
		body := make([]byte, 4096)
		read, _ := response.Body.Read(body)
		response.Body.Close()
		page := string(body[:read])
		title := "Notes"
		if strings.Contains(page, "Note 42") {
			title = "Note 42"
		}
		fmt.Printf("%s title=%q cache=%q\n", path, title, response.Header.Get("Cache-Control"))
	}
}
```

with a shell at `public/index.html`:

```html
<!doctype html>
<html>
<head><title>Notes</title></head>
<body><div id="root"></div></body>
</html>
```
