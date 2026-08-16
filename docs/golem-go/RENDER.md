# Frontend hosting and link-preview metadata

Status: **implemented**.

The package hosts a compiled single-page application next to Golem's GraphQL
handler and injects per-route Open Graph and Twitter metadata so links unfurl.
It is HTTP transport in the same sense as `graphql`: it owns request
classification, caching headers, and safe rendering. It does not own data
access — every fact that appears in a link preview is fetched by application
code through the ordinary generated caller API.

## Package and dependencies

New package `go/render` (`github.com/eleven-am/golem/go/render`). Standard
library only: `net/http`, `io/fs`, `os`, `path`, `strings`, `net/url`, `html`,
`context`. No router dependency — Go 1.22+ `http.ServeMux` patterns
(`/post/{id}`) cover the route shapes this contract supports, and the module is
on Go 1.25. One TypeScript shape does not port: `http.ServeMux` requires a
wildcard to be a complete path segment, so an Express pattern that embeds one
inside a segment (`/m=:name`) cannot be expressed and is refused at startup
with `CodeRouteInvalid`. Several whole-segment patterns may still share one
resolver (`/movie/{name}` and `/film/{name}`). No static-file dependency — `http.FileServer` is
deliberately not used because its directory indexes, trailing-slash redirects,
and `index.html` special-casing contradict this contract; a bounded file
responder over `http.ServeContent` (~60 lines) replaces Express `serve-static`.

The package imports nothing from `golem`, `runtime`, or generated code. It
cannot: `Caller[P]` types are generated per application, so the render package
is generic-free and policy-agnostic by construction, exactly as `graphql`
owns HTTP but not authorization.

## Public API

```go
type Meta struct {
	Title       string
	Description string
	Image       string
	URL         string
	Tags        map[string]string
	Omit        []string
}

type Link struct {
	Path   string
	Values map[string]string
	Query  url.Values
	URL    string
}

type Resolver func(ctx context.Context, link Link) (*Meta, error)

type Route struct {
	Patterns []string
	Resolve  Resolver
}

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

func New(config Config) (*Renderer, error)

type Renderer struct{ /* unexported */ }

func (renderer *Renderer) Handler() http.Handler

type ErrorCode string

const (
	CodeConfigInvalid ErrorCode = "RENDER_CONFIG_INVALID"
	CodeBundleMissing ErrorCode = "RENDER_BUNDLE_MISSING"
	CodeIndexInvalid  ErrorCode = "RENDER_INDEX_INVALID"
	CodeRouteInvalid  ErrorCode = "RENDER_ROUTE_INVALID"
)

func CodeOf(err error) (ErrorCode, bool)
```

That is the entire exported surface: eight types, two functions, one method,
four codes. Every exported symbol carries godoc. `CodeOf` recognizes failures
through ordinary error wrapping and never classifies from public text, matching
`queryplan.CodeOf` and `embedding.CodeOf`.

The package imports nothing from Golem. It is transport: a file server and a
tag injector. A resolver is an ordinary function the application supplies, and
how that function obtains its facts is invisible here.

Field semantics:

- Exactly one of `Dir` (a bundle directory on disk) or `Files` (any `fs.FS`,
  typically an `embed.FS` for single-binary deployment) must be set.
- `Index` defaults to `index.html`, resolved inside the bundle.
- `Reserved` defaults to `{"/api", "/graphql"}`. Entries are normalized to a
  leading `/` with trailing slashes trimmed.
- `Defaults` is the metadata rendered when no route matches, a resolver
  returns `nil`, or a resolver fails. The zero `Meta` means "no defaults": the
  shell is then served byte-identical to the parsed original.
- In `Meta`, the empty string means unset. `Tags` merges over the
  shorthand-generated tags; `Omit` deletes generated tags by name (the Go
  replacement for the TypeScript `null` tag value, which `map[string]string`
  cannot express).
- `BaseURL`, when set, must parse as an absolute URL and expands relative
  `URL`/`Image` values. Unset, relative URLs pass through unchanged. URL
  values are never derived from the request `Host` header.
- A `Route` registers one resolver under one or more `http.ServeMux` path
  patterns. Multiple patterns sharing one resolver replaces the stacked
  `@RenderRoute` decorators.
- A `Link` carries everything a preview may legitimately read and nothing else.
  `Values` holds the pattern wildcards resolved by `http.ServeMux`, so
  `/f/{token}` yields `Values["token"]`. `Query` carries capability parameters.
  `URL` is absolute when `BaseURL` is set and otherwise the request path. The
  request itself is deliberately not passed: no cookie or credential header is
  meaningful when the caller is a crawler, and handing a resolver a request
  invites code written as though a user were present.
- `ReportResolverError` receives resolver errors and recovered panics. `nil`
  discards them. This matches the explicit-callback convention of
  `graphql.Config.ReportInternalError` rather than ambient logging.

### Smallest complete usage

```go
renderer, err := render.New(render.Config{Dir: "dist-web/client"})
if err != nil {
	return err
}
mux := http.NewServeMux()
mux.Handle("/graphql", principalMiddleware(application, graph.Handler()))
mux.Handle("/", renderer.Handler())
```

`http.ServeMux` precedence sends `/graphql` to the backend handler before the
render handler ever sees it; `Reserved` is the belt to that suspender.

### Per-route metadata

```go
renderer, err := render.New(render.Config{
	Dir:     "dist-web/client",
	BaseURL: "https://social.example",
	Defaults: render.Meta{
		Title:       "Social",
		Description: "A tiny example network.",
		Image:       "/og-default.png",
	},
	Routes: []render.Route{{
		Patterns: []string{"/post/{id}", "/p/{id}"},
		Resolve: func(ctx context.Context, link render.Link) (*render.Meta, error) {
			caller, err := application.ForPrincipal(ctx, social.Principal{})
			if err != nil {
				return nil, err
			}
			id, err := golem.ParseUUID(link.Values["id"])
			if err != nil {
				return nil, nil
			}
			row, err := caller.Posts.FindUnique(ctx,
				social.Posts.ByID.Value(id),
				social.Posts.Select(social.Posts.Title),
			)
			if err != nil {
				return nil, nil
			}
			title, _ := golem.Value(row, social.Posts.Title).Get()
			return &render.Meta{
				Title: title,
				Tags:  map[string]string{"og:type": "article"},
			}, nil
		},
	}},
	ReportResolverError: func(ctx context.Context, err error) {
		log.Print("render resolver failed")
	},
})
```

The TypeScript package can vary a preview by user agent, serving a wide image
to Twitterbot specifically. A `Link` carries no headers, so v0.3 cannot. This
is deliberate: the first header admitted invites the next, and a preview that
differs by crawler is a preview that cannot be checked by looking at it. If the
capability is genuinely missed, a single named field is a smaller change than
reopening the request.

## Writing a resolver

A link preview is unfurled by a crawler holding nothing but a URL. There is no
session, no cookie, and no principal. Whatever a resolver returns is visible to
anyone who has the link, and to every service that follows it.

This package cannot check that for you. It never touches data; it calls the
function you supply and injects what comes back. The guidance below is
therefore addressed to resolver authors, not enforced by `render`.

**Decide in policy, not in the resolver.** An application on Golem has already
declared who may read what. A resolver should obtain a caller for the
application's anonymous principal and perform an ordinary authorized read:

```go
caller, err := application.ForPrincipal(ctx, social.Principal{})
row, err := caller.Posts.FindUnique(ctx, social.Posts.ByID.Value(id),
	social.Posts.Select(social.Posts.Title))
if err != nil {
	return nil, nil
}
```

There is no `if post.Published` in that code, and there should not be. If the
post is not readable by an anonymous actor, the read returns nothing, the
resolver returns `nil`, and the crawler receives the configured defaults.

Four properties follow, and none of them require render to know anything:

- **Field masking is free.** `CannotReadFields` lets a shared row unfurl its
  title while its body stays withheld, declared once rather than remembered at
  every call site.
- **Policy changes propagate.** Unpublishing a post changes what unfurls with
  no resolver edit.
- **Previews cannot drift from the rest of the application.** The rules that
  govern the logged-out web page govern the preview, because they are the same
  rules.
- **Reads are observable.** A caller read emits the ordinary `read.*`
  observations; a policy refusal is already visible as a refusal.

**Capability links are the case that legitimately reveals more.** When the URL
itself is the grant — `/f/{token}` — the token is in `Link.Values` and policy
can be written against it, so an anonymous actor may read exactly the row the
token names. That is a policy expression, not a resolver exception.

**Reaching past policy is possible and is the application's choice.** A
resolver is an ordinary closure; nothing stops it holding `App.System()`. If
you do that, you have taken the disclosure decision out of your policy and into
that function, where no other part of the system can see it.

## Behavior specification

Request handling is a fixed classification order. Each step either responds or
falls to the next:

1. **Reserved paths.** A path equal to a reserved entry or under it
   (`/api`, `/api/...`) is answered `404 Not Found`. The render handler never
   serves the shell or a file for a reserved path. In normal composition the
   backend's more specific mux registration wins first and render never sees
   these requests; the 404 guarantees render cannot swallow them when it does.
2. **Methods.** Anything other than `GET` and `HEAD` is `404`. `HEAD` is
   served everywhere `GET` is, with identical headers (including
   `Content-Length` and `Content-Type`) and no body.
3. **Dotfiles.** A path containing any segment starting with `.` is `404`,
   whether or not a file exists.
4. **Static files.** An existing regular file in the bundle at the request
   path is served via `http.ServeContent` with:
   - `Cache-Control: public, max-age=31536000, immutable` when the filename is
     content-hashed — final `-` or `.` separated token of the stem, length ≥ 8,
     `[A-Za-z0-9_]+`, and either all-hex or containing an uppercase letter,
     digit, or underscore (ported heuristic);
   - `Cache-Control: no-cache` otherwise.
   Directories are never listed, never redirected, and never served; a
   directory path falls through to classification below. The index file's own
   request path (`/index.html`) is excluded from static serving and falls
   through, so the raw unrendered index is never served.
5. **Metadata routes.** A registered pattern match runs its resolver and
   serves the rendered shell (`Content-Type: text/html; charset=utf-8`,
   `Cache-Control: no-cache`). An explicit route beats the asset heuristics
   below.
6. **Asset heuristics.** With no file present, the request is `404` — not the
   shell — when any of: the path is `/assets` or under `/assets/`; the
   extension is in the known asset set (`.js`, `.css`, `.map`, `.png`,
   `.woff2`, … ported verbatim); or `Sec-Fetch-Dest` names a subresource
   destination (`script`, `style`, `image`, `font`, …). `Sec-Fetch-Dest:
   document` forces non-asset classification.
7. **Content negotiation.** A request whose `Accept` header is present and
   accepts neither `text/html`, `application/xhtml+xml`, nor `*/*` is `404`.
8. **Shell fallback.** Everything else — deep browser routes — is the shell
   rendered with `Defaults`, `Cache-Control: no-cache`.

Resolver caching remains the resolver's business, exactly as the TypeScript
README argues: Golem does not cache metadata resolution and will not. Only the
resolver's author knows which request inputs select the preview.

## Startup validation

`New` validates everything and returns a typed error naming the failure;
nothing is deferred to the first request:

- `CodeConfigInvalid`: both or neither of `Dir`/`Files` set; `BaseURL` not an
  absolute URL; a reserved entry empty after normalization.
- `CodeBundleMissing`: the bundle directory or index file absent or not a
  regular file. For `Dir`, the error message names the resolved absolute index
  path; for `Files`, the fs-relative path.
- `CodeIndexInvalid`: the index escapes the bundle directory, or contains no
  closing `</head>` tag.
- `CodeRouteInvalid`: an empty pattern list, a pattern not starting with `/`,
  a pattern `http.ServeMux` rejects, a duplicate pattern, a pattern under a
  reserved prefix, or a `nil` resolver.

## Metadata injection

The index file is read and parsed exactly once inside `New` and never
modified. Parsing splits at the first case-insensitive `</head>` into a prefix
and suffix, and precomputes a second prefix with the first `<title>` element
removed. Every response is rendered in memory from these immutable pieces —
the shell on disk is never rewritten, matching the TypeScript `HtmlShell`.

Rendering a `Meta` (route result merged over `Defaults`, `Tags` merged over
both):

- When no defaults exist and the resolver returned `nil`, the original
  document is emitted byte-identical.
- Shorthand fields expand to `<title>`, `description`, `og:type` (`website`),
  `og:url`, `og:title`, `og:description`, `og:image`, `twitter:card`
  (`summary_large_image`), `twitter:title`, `twitter:description`,
  `twitter:image`. `Tags` entries override any of these; `Omit` deletes them.
- Names beginning `og:` or `article:` render as `property=` attributes,
  everything else as `name=`. All names and values pass through
  `html.EscapeString` before interpolation.
- `og:url` uses `Meta.URL` when set, otherwise the request URL (path and
  query). With `BaseURL` configured, `URL` and `Image` are expanded via
  `url.URL.Parse` resolution; unparseable values pass through unchanged.
- Existing head content is preserved untouched; injected tags are appended
  immediately before `</head>`. The shell's own `<title>` is removed only when
  a title is actually being rendered, so a metadata-less response keeps the
  original title.

## Failure semantics

- **Fails at startup:** everything in the validation list above. A missing
  bundle is a deployment error and must halt the process with the resolved
  path in the message, not degrade.
- **Degrades to defaults, never 500:** a resolver returning `nil`, returning
  an error, or panicking renders `Defaults`. Errors and recovered panics are
  passed to `ReportResolverError` (discarded when nil) and never reach the
  response. The shell path cannot produce a 500 after successful startup.
- **404, not degradation:** missing assets, dotfiles, reserved paths,
  non-GET/HEAD methods, non-HTML requests. These are real 404s so crawlers and
  CDNs cache absence correctly instead of caching the shell under an asset URL.
- **Observations: none in v0.3.** `observe` is a closed, validated vocabulary
  (`observe/observe.go`); adding a render kind would touch the kind/operation
  sets, the telemetry manifest, and the otel/slog adapters. That cost buys
  nothing yet: resolver reads through the anonymous caller already emit
  ordinary `read.*` observations, and static/shell serving is plain HTTP best
  measured by the application's own HTTP middleware. If evidence later demands
  it, the addition is one `KindRender` and one `render.meta` operation with
  `success`/`failure` outcomes, following the manifest procedure.

