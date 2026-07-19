# Design Brief — `@eleven-am/golem-render`

Date: 2026-07-19
Status: designed, not implemented. Approved in principle; Phase 1 ready to build.
Author of record: design discussion with Roy Ossai.

---

## 1. Why this exists

Two applications have now hand-written the same layer, in two different shapes:

- **frames** (`old/backend` in `frames-go`) — NestJS + EJS. A single `views/index.ejs` shell, a `RenderController` mapping URL patterns to metadata, and a `RenderService` resolving titles and posters from Prisma and TMDB.
- **readable-v3** (`src/platform/production-server.ts`) — NestJS + Express middleware. Static asset serving, SPA fallback, cache headers, reserved backend paths. No metadata layer.

Neither is application logic. Both are the same boilerplate: *serve a built single-page app, and put useful things in its `<head>`*. Every future Golem application will need it. That is the case for a package.

The goal is that an application author never writes a static-file handler, a SPA fallback, or an HTML template again — they point Golem at a build directory and, optionally, declare what metadata each route should carry.

---

## 2. Scope — two separable halves

### Half 1 — SPA hosting

Serve a built frontend: static assets with correct caching, wildcard fallback to the shell so a hard refresh of a client-side route does not 404, real 404s for missing assets, and no interference with backend routes.

No authorization dimension — these are static bytes. Every Golem app needs this. **readable-v3 needs this today** and can adopt it immediately.

### Half 2 — Route metadata

Resolve `{ title, description, image }` for a URL and inject Open Graph and Twitter tags into the shell's `<head>`, so links unfurl correctly in Slack, iMessage, Twitter, Discord, and for crawlers.

Useful wherever links get shared. frames was built entirely for this.

The halves ship in that order. Half 1 is independently valuable and carries no risk.

---

## 3. Decisions already made

These were settled during design. **Do not re-litigate them without talking to Roy** — each was considered and chosen deliberately.

### 3.1 Metadata always renders, for everyone

Tags are emitted regardless of whether the fetcher is authenticated. An anonymous Slack unfurler gets the real title and description of the resource.

This means a render resolver reads data **unscoped**, using the plain client, and the application author decides what is safe to put in a tag. That is consistent with Golem's existing design principle: scoping is engine-added only for what Golem executes *on behalf of a caller*; developer-written queries are expert territory with no policy magic. frames did exactly this — `RenderService` used `this.prisma` directly, never a scoped client.

The accepted trade: whoever holds a URL can see its title and description without logging in. This is the same trade every "anyone with the link" share makes.

**The shell always renders**, unconditionally — authenticated route, private resource, no cookie at all. The SPA boots and handles its own auth. A render layer that 404s or redirects unauthenticated requests is broken.

### 3.2 URLs pass through verbatim

Whatever a resolver returns for `image` goes into the tag unchanged. Relative (`/og-default.png`) and absolute (`https://image.tmdb.org/…`) both work — every major unfurler resolves relative URLs against the page URL, and frames has run this way in production.

Consequence, and a benefit: **no origin is ever derived from the `Host` header**, so there is no attacker-controlled value reaching the output.

`baseUrl` is available as optional config for anyone who wants relative values expanded. It does nothing when unset.

### 3.3 The build directory is whatever the bundler emitted

Require nothing beyond *a directory containing `index.html`*. Do not mandate an `assets/` folder or any layout — the first app that configures `assetsDir` would break.

readable's Vite output, as a concrete example:

```
dist-web/client/
├── index.html
└── assets/
    ├── index-CDc0fz_X.js       # entry, content-hashed
    ├── index-CLuvk8pl.css
    ├── article._id-bEVtFhLl.js # route chunks, code-split
    └── … ~60 more
```

`index.html` references assets **absolutely** (`/assets/index-CDc0fz_X.js`), which is Vite's default `base: '/'`. This is what allows one shell to be served at any URL depth — `/` and `/article/abc123` get identical HTML and both resolve their assets.

### 3.4 Nest owns invocation

**This is the most important constraint in this document.**

A function the developer writes and decorates must be invoked by **Nest**, not by Golem. Discovery is not integration: `DiscoveryService` gives Golem the instance (so constructor injection works), but if Golem then calls `method.bind(instance)` itself, the function is just a callback and everything Nest applies at invocation time is lost — param decorators, guards, pipes, interceptors, exception filters, and request-scoped providers.

`@ComputedField` made this mistake and every defect in issue #7 is downstream of it. `@CustomQuery` gets it right by applying Nest's `Query()`.

For this package it means `@RenderRoute` **composes Nest's own decorators**:

```ts
export function RenderRoute(path: string): MethodDecorator {
  return applyDecorators(
    Get(path),
    UseInterceptors(RenderInterceptor),
  );
}
```

Consequences, all of them good:

- `@Param`, `@Query`, `@Headers`, `@Req`, `@Ip` work — Nest's own, imported from `@nestjs/common`. No lookalike decorators shipped by this package, no `ROUTE_ARGS_METADATA` spelunking, no internal Nest constants relied upon.
- Pipes work: `@Param('id', ParseUUIDPipe)`.
- Guards, interceptors, and filters work.
- Route resolution is Nest's, so `/article/new` vs `/article/:id` follows Nest's ordering rules rather than a specificity scorer written here.

An earlier draft of this design proposed reading `ROUTE_ARGS_METADATA` and resolving params manually. **That approach was rejected** — it is precisely the `@ComputedField` failure mode of half-reimplementing Nest.

---

## 4. The API

### Level 1 — hosting only

```ts
imports: [
  GolemRenderModule.forRoot({
    client: 'dist-web/client',
  }),
]
```

Static assets, SPA fallback, reserved backend paths, correct caching. Nothing else required. This alone replaces readable's `production-server.ts`.

### Level 2 — add defaults

```ts
GolemRenderModule.forRoot({
  client: 'dist-web/client',
  defaults: {
    title: 'Readable',
    description: 'Save anything. Read it later. Listen anywhere.',
    image: '/og-default.png',
  },
})
```

Every route now emits OG and Twitter tags. Still no resolvers.

### Level 3 — per-route metadata

```ts
@Controller()
export class ArticleRender {
  constructor(private readonly prisma: PrismaService) {}

  @RenderRoute('/article/:id')
  async article(
    @Param('id') id: string,
    @Headers('user-agent') agent: string,
  ) {
    const article = await this.prisma.article.findUnique({
      where: { id },
      select: { title: true, excerpt: true, image: true, wideImage: true },
    });

    if (!article) return null;

    return {
      title: article.title,
      description: article.excerpt,
      image: /Twitterbot/i.test(agent ?? '') ? article.wideImage : article.image,
    };
  }

  @RenderRoute('/m=:name')
  @RenderRoute('/movie/:name')
  async movie(@Param('name') name: string) {
    // stacked decorators: several patterns, one handler
  }
}
```

Notes:

- The developer writes `@Controller()` themselves. This is deliberate — it is a real Nest controller, and pretending otherwise would mean reimplementing Nest.
- Returning `null`, returning `undefined`, or throwing all fall back to `defaults`. **A failed lookup must never break the page.** frames encoded this as `.orElse(() => this.getDefaults())` on every single resolver; it is the correct posture and must be preserved.
- Throws are logged, never surfaced to the client.
- `=` in a pattern is just a literal, so frames' URL scheme (`m=`, `s=`, `p=`, `c=`, `r=`, `f=`, `w=`, `col=`, `pl=`) ports unchanged.

### The return shape

```ts
interface MetaTags {
  title?: string;
  description?: string;
  image?: string;
  url?: string;
  meta?: Record<string, string | null>;
}
```

`meta` is an escape hatch merged **over** whatever the shorthand fields produced. It can add tags, override defaults, or drop a tag by setting it to `null`:

```ts
return {
  title: article.title,
  description: article.excerpt,
  image: article.image,
  meta: {
    'og:type': 'article',
    'article:published_time': article.publishedAt.toISOString(),
    'twitter:card': 'summary',   // overrides the default
  },
};
```

Same escaping applies to everything, shorthand and `meta` alike.

### What lands in the HTML

The app's `index.html` is **never modified on disk**. Golem rewrites the head per request:

```html
  <head>
    <meta charset="UTF-8" />
-   <title>Readable</title>
+   <title>Jackie Chan — Wikipedia</title>
+   <meta name="description" content="Hong Kong actor and martial artist…">
+   <meta property="og:type" content="website">
+   <meta property="og:url" content="/article/abc123">
+   <meta property="og:title" content="Jackie Chan — Wikipedia">
+   <meta property="og:description" content="Hong Kong actor and martial artist…">
+   <meta property="og:image" content="/covers/abc.jpg">
+   <meta name="twitter:card" content="summary_large_image">
+   <meta name="twitter:title" content="Jackie Chan — Wikipedia">
+   <meta name="twitter:description" content="Hong Kong actor and martial artist…">
+   <meta name="twitter:image" content="/covers/abc.jpg">
    <script type="module" crossorigin src="/assets/index-CDc0fz_X.js"></script>
```

---

## 5. Implementation phases

Each phase is independently shippable. Stop at each checkpoint.

### Phase 1 — static hosting

Port the contract from `readable-v3/src/platform/production-server.ts`, which already encodes the details that only show up in production:

1. **Reserved backend paths** — `/api` and `/graphql` fall through instead of being swallowed by the SPA fallback. Configurable, with that pair as the default.
2. **Asset requests never fall back** — anything with a file extension, or under the assets path, that does not exist returns a real 404. Without this, a stale chunk request receives `index.html` with a `200`, and the developer debugs `Unexpected token '<'` for an hour.
3. **`Accepts: html` gate** — an XHR to a missing path gets a 404, not a page.
4. **Cache split** — `public, max-age=31536000, immutable` for content-hashed assets; `no-cache` for the shell.
5. **Boot-time existence check** — if `index.html` is missing, fail at startup with the **resolved absolute path** in the message, not the relative one that was configured.
6. `dotfiles: 'ignore'`, no directory redirects, no directory index.

Config surface: `client`, `reserved` (default `['/api', '/graphql']`), `index` (default `'index.html'`).

**Checkpoint: readable-v3 deletes `src/platform/production-server.ts` and behaves identically.** That is the proof the extraction is faithful. Do not proceed to Phase 2 before this passes.

Tests: reserved paths fall through; missing asset 404s rather than serving HTML; a deep client-side route serves the shell; XHR to a missing path 404s; each cache header class is correct; boot fails loudly and informatively on a missing bundle.

### Phase 2 — defaults and head injection

Parse `index.html` **once at boot** into a prefix/suffix pair around the injection point, so per-request work is string concatenation rather than HTML parsing.

- Replace the existing `<title>`; inject the tag block before `</head>`.
- **Escape every interpolated value for attribute context.** A title containing `"` must not break out of `content="…"`. This is the one place this feature can become an injection vector; it gets its own tests with hostile titles. Use `escape-html` rather than hand-rolling.
- `index.html` must be served `no-cache` — mandatory now, since the bytes vary per route.
- Consequence: the shell can no longer be served by `sendFile`; it is generated per request. Static assets stay on the untouched fast path.

Tests: tags present and correct; a hostile title or description cannot escape the attribute; the shell is `no-cache`; the file on disk is unmodified; `meta` overrides and `null` deletions behave.

### Phase 3 — `@RenderRoute`

A decorator and an interceptor, per §3.4. The SPA fallback middleware must be registered **after** the controllers so declared routes win.

- Stacked decorators on one handler.
- `null` / `undefined` / throw → defaults, throws logged.
- Route ordering is Nest's; document that a wildcard must be declared last, which is the same rule frames already lived with.

Tests: metadata resolves and injects; stacked patterns both match; `=` patterns work; null, undefined, and throw all fall back to defaults and never 500; an unmatched route gets defaults; `@Param`/`@Query`/`@Headers` resolve; a guard on a render route actually denies.

---

## 6. Package placement

`packages/render` in the golem monorepo → `@eleven-am/golem-render`. Same build, test, and release train as `@eleven-am/golem-queue`: `tsc` build, colocated `*.test.ts`, ts-jest, CommonJS, extensionless relative imports.

Add it to the root `build` script and to the publish workflow's dependency-ordered `publish` list.

**No dependency on `@eleven-am/golem-core`.** This package is orthogonal to policy — an app can use either without the other, and an app behind a CDN never needs to install it.

Dependencies: `path-to-regexp` (if any matching is needed beyond Nest's own) and `escape-html`. Both battle-tested; hand-rolling either is how subtly wrong escaping happens.

---

## 7. Open questions

1. **`cookies` on the context.** Reachable through `@Req()`. Worth a first-class decorator only if a real use case appears.
2. **Compression.** Currently assumed to be the application's concern (`compression` middleware or an upstream proxy). Revisit only if measurement justifies it.
3. **Metadata caching.** frames performs a database hit plus a TMDB call on every render, including on the wildcard path, and crawlers hit those URLs hard. No caching is specified here. If it becomes a problem, cache resolved `MetaTags` by path with a short TTL — but measure first.

---

## 8. Appendix — a bug to fix during a frames port

Not a Golem concern, but it will be encountered by whoever ports frames and should be fixed rather than carried across.

In `old/backend/src/render/render.service.ts`, the person, collection, and company resolvers pass TMDB's raw path fields directly into the poster field:

```ts
poster: person.profile_path,     // "/abc123.jpg"
poster: collection.poster_path,
poster: company.logo_path,
```

These resolve against the frames origin rather than TMDB's CDN. The episode resolver does it correctly:

```ts
poster: episode.still_path ? `https://image.tmdb.org/t/p/original${episode.still_path}` : media.poster,
```

`media.poster` from the frames database is stored complete and is fine. Only the three raw TMDB path fields are affected — link previews for people, collections, and companies would show a broken image.

Pass-through remains the correct behaviour for the render layer; this is the application supplying a bad value.
