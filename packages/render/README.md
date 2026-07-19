# @eleven-am/golem-render

Nest-native hosting for a compiled single-page application, with optional per-route Open Graph and Twitter metadata. It serves the frontend shell without taking over backend routes and lets ordinary Nest controllers resolve link-preview metadata.

## Install

```bash
npm install @eleven-am/golem-render
```

The package currently targets Nest's Express adapter.

## Host a frontend

```ts
import { Module } from '@nestjs/common';
import { GolemRenderModule } from '@eleven-am/golem-render';

@Module({
  imports: [
    GolemRenderModule.forRoot({
      client: 'dist-web/client',
    }),
  ],
})
export class AppModule {}
```

`client` can use any bundle layout; it only needs to contain `index.html`. The module:

- serves existing static files without directory indexes, redirects, or dotfiles;
- gives content-hashed assets a one-year immutable cache and other files `no-cache`;
- serves the shell for deep browser routes and HEAD requests;
- returns real 404s for missing assets, non-HTML requests, and non-GET requests;
- leaves `/api` and `/graphql` to backend handlers by default;
- fails at startup with the resolved absolute path when the bundle is missing.

Change the reserved prefixes or index filename explicitly when necessary:

```ts
GolemRenderModule.forRoot({
  client: 'frontend/output',
  index: 'shell.html',
  reserved: ['/api', '/graphql', '/health'],
})
```

## Default metadata

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

The index file is read and parsed once at startup but is never modified. Each shell response is rendered in memory and sent with `Cache-Control: no-cache`. Relative URLs pass through unchanged. Set `baseUrl` only when they should be expanded to absolute URLs.

## Route metadata

`@RenderRoute` is a real Nest GET route. Put it on a method in an ordinary controller and use Nest's own parameter decorators, pipes, guards, interceptors, filters, and request-scoped dependencies.

```ts
import { Controller, Headers, Param } from '@nestjs/common';
import { GolemRenderModule, RenderRoute } from '@eleven-am/golem-render';

@Controller()
export class ArticleRenderController {
  constructor(private readonly prisma: PrismaService) {}

  @RenderRoute('/article/:id')
  async article(
    @Param('id') id: string,
    @Headers('user-agent') userAgent: string,
  ) {
    const article = await this.prisma.article.findUnique({
      where: { id },
      select: { title: true, excerpt: true, image: true, wideImage: true },
    });
    if (!article) return null;
    return {
      title: article.title,
      description: article.excerpt,
      image: /Twitterbot/i.test(userAgent ?? '') ? article.wideImage : article.image,
    };
  }
}
```

The application owns the query and its disclosure policy. Metadata routes do not add caller authorization or Golem policy scoping. This makes anonymous link unfurling possible; return only information that is safe for anyone holding the URL.

Returning `null` or `undefined`, or throwing, renders the configured defaults. Errors are logged and never turn the shell into a 500 response. The SPA shell renders without authentication and handles its own application authentication after boot.

Multiple URL patterns can share one method:

```ts
@RenderRoute('/m=:name')
@RenderRoute('/movie/:name')
movie(@Param('name') name: string) {
  return { title: name };
}
```

Nest owns route ordering. Declare wildcard routes last, as with any other Nest controller.

## Custom metadata

```ts
return {
  title: article.title,
  description: article.excerpt,
  image: article.image,
  meta: {
    'og:type': 'article',
    'article:published_time': article.publishedAt.toISOString(),
    'twitter:card': 'summary',
    'og:description': null,
  },
};
```

`meta` is merged over the shorthand-generated tags. A `null` value deletes a tag. All names and values are HTML-escaped before interpolation. URL values are never derived from the request `Host` header.

## License

GPL-3.0
