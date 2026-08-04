import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  ArgumentsHost,
  CallHandler,
  CanActivate,
  Catch,
  Controller,
  ExecutionContext,
  ExceptionFilter,
  ForbiddenException,
  Get,
  Headers,
  Injectable,
  Logger,
  Module,
  Param,
  ParseIntPipe,
  Query,
  Scope,
  NestInterceptor,
  UseGuards,
  UseFilters,
  UseInterceptors,
} from '@nestjs/common';
import type { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import request from 'supertest';
import { map, Observable } from 'rxjs';
import { RenderRoute } from './decorators';
import { GolemRenderModule } from './render.module';

const CLIENT = resolve(__dirname, '../test/fixture');
const INDEX = resolve(CLIENT, 'index.html');

@Injectable()
class DenyGuard implements CanActivate {
  canActivate(_context: ExecutionContext): boolean {
    return false;
  }
}

@Injectable()
class ThrowingGuard implements CanActivate {
  canActivate(): never {
    throw new ForbiddenException('filtered denial');
  }
}

@Catch(ForbiddenException)
class DenialFilter implements ExceptionFilter {
  catch(exception: ForbiddenException, host: ArgumentsHost): void {
    host.switchToHttp().getResponse().status(418).send({ filtered: exception.message });
  }
}

@Injectable()
class TitleInterceptor implements NestInterceptor {
  intercept(_context: ExecutionContext, next: CallHandler): Observable<unknown> {
    return next.handle().pipe(map((metadata) => ({
      ...(metadata as object),
      title: `Intercepted ${(metadata as { title: string }).title}`,
    })));
  }
}

let nextOrdinal = 0;

@Injectable({ scope: Scope.REQUEST })
class RequestOrdinal {
  readonly value = ++nextOrdinal;
}

@Controller()
class TestController {
  constructor(private readonly requestOrdinal: RequestOrdinal) {}

  @Get('/api/ping')
  ping(): object {
    return { ok: true };
  }

  @Get('/known')
  known(): object {
    return { controller: true };
  }

  @RenderRoute('/article/:id')
  article(
    @Param('id', ParseIntPipe) id: number,
    @Query('description') description: string,
    @Headers('user-agent') userAgent: string,
  ) {
    return {
      title: `Article ${id}`,
      description,
      meta: { 'x-user-agent': userAgent },
    };
  }

  @RenderRoute('/m=:name')
  literalEquals(@Param('name') name: string) {
    return { title: name };
  }

  @RenderRoute('/first/:name')
  @RenderRoute('/second/:name')
  stacked(@Param('name') name: string) {
    return { title: `Stacked ${name}` };
  }

  @RenderRoute('/empty')
  empty(): null {
    return null;
  }

  @RenderRoute('/undefined')
  undefinedResult(): undefined {
    return undefined;
  }

  @RenderRoute('/failure')
  failure(): never {
    throw new Error('lookup failed');
  }

  @UseGuards(DenyGuard)
  @RenderRoute('/denied')
  denied() {
    return { title: 'Must not render' };
  }

  @UseInterceptors(TitleInterceptor)
  @RenderRoute('/intercepted')
  intercepted() {
    return { title: 'route' };
  }

  @UseFilters(DenialFilter)
  @UseGuards(ThrowingGuard)
  @RenderRoute('/filtered')
  filtered() {
    return { title: 'Must not render' };
  }

  @RenderRoute('/request-scope')
  requestScope() {
    return { title: `Request ${this.requestOrdinal.value}` };
  }
}

@Module({
  controllers: [TestController],
  providers: [
    DenyGuard,
    ThrowingGuard,
    DenialFilter,
    TitleInterceptor,
    RequestOrdinal,
  ],
  imports: [
    GolemRenderModule.forRoot({
      client: CLIENT,
      defaults: {
        title: 'Default title',
        description: 'Default description',
        image: '/default.png',
      },
    }),
  ],
})
class TestModule {}

describe('@eleven-am/golem-render (e2e)', () => {
  let app: INestApplication;
  let originalIndex: string;

  beforeAll(async () => {
    originalIndex = readFileSync(INDEX, 'utf8');
    const moduleRef = await Test.createTestingModule({ imports: [TestModule] }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
  });

  afterAll(async () => {
    await app.close();
  });

  it('registers the SPA fallback after controllers and preserves reserved routes', async () => {
    await request(app.getHttpServer()).get('/known').expect(200, { controller: true });
    await request(app.getHttpServer()).get('/api/ping').expect(200, { ok: true });
    await request(app.getHttpServer()).get('/api/missing').set('accept', 'text/html').expect(404);
    await request(app.getHttpServer()).get('/graphql/missing').set('accept', 'text/html').expect(404);
  });

  it('serves deep client routes and never modifies the index on disk', async () => {
    const response = await request(app.getHttpServer())
      .get('/deep/client/route')
      .set('accept', 'text/html')
      .set('host', 'attacker.example')
      .expect(200)
      .expect('cache-control', 'no-cache');

    expect(response.type).toBe('text/html');
    expect(response.text).toContain('<div id="root"></div>');
    expect(response.text).toContain('<title>Default title</title>');
    expect(response.text).not.toContain('attacker.example');
    expect(readFileSync(INDEX, 'utf8')).toBe(originalIndex);

    const explicitIndex = await request(app.getHttpServer())
      .get('/index.html')
      .set('accept', 'text/html')
      .expect(200)
      .expect('cache-control', 'no-cache');
    expect(explicitIndex.text).toContain('<title>Default title</title>');
    expect(explicitIndex.text).not.toContain('<title>Fixture application</title>');
  });

  it('does not turn missing assets, dotfiles, or XHR paths into successful HTML', async () => {
    await request(app.getHttpServer()).get('/assets/missing.js').set('accept', 'text/html').expect(404);
    await request(app.getHttpServer()).get('/assets').set('accept', 'text/html').expect(404);
    await request(app.getHttpServer()).get('/.secret').set('accept', 'text/html').expect(404);
    await request(app.getHttpServer()).get('/missing').set('accept', 'application/json').expect(404);
    await request(app.getHttpServer()).post('/missing').set('accept', 'text/html').expect(404);
  });

  it('serves client routes whose segments contain dots', async () => {
    for (const path of ['/user/john.doe', '/authors/j.k.rowling', '/article/my.slug']) {
      const response = await request(app.getHttpServer())
        .get(path)
        .set('accept', 'text/html')
        .expect(200);
      expect(response.text).toContain('<div id="root"></div>');
    }
  });

  it('trusts sec-fetch-dest over the path when the browser sends it', async () => {
    await request(app.getHttpServer())
      .get('/missing/chunk')
      .set('accept', 'text/html')
      .set('sec-fetch-dest', 'script')
      .expect(404);

    const navigation = await request(app.getHttpServer())
      .get('/reports/2024.q1')
      .set('accept', 'text/html')
      .set('sec-fetch-dest', 'document')
      .expect(200);
    expect(navigation.text).toContain('<div id="root"></div>');
  });

  it('splits immutable hashed-asset caching from non-hashed files', async () => {
    await request(app.getHttpServer())
      .get('/assets/app-AbCd1234.js')
      .expect(200)
      .expect('cache-control', 'public, max-age=31536000, immutable');
    await request(app.getHttpServer())
      .get('/assets/plain.js')
      .expect(200)
      .expect('cache-control', 'no-cache');
    await request(app.getHttpServer())
      .get('/chunk-AbCd1234.js')
      .expect(200)
      .expect('cache-control', 'public, max-age=31536000, immutable');
    await request(app.getHttpServer())
      .get('/application-production.js')
      .expect(200)
      .expect('cache-control', 'no-cache');
  });

  it('uses normal Nest params, queries, headers, and pipes on render routes', async () => {
    const response = await request(app.getHttpServer())
      .get('/article/42?description=Useful')
      .set('user-agent', 'RenderBot')
      .expect(200)
      .expect('cache-control', 'no-cache');

    expect(response.text).toContain('<title>Article 42</title>');
    expect(response.text).toContain('content="Useful"');
    expect(response.text).toContain('<meta name="x-user-agent" content="RenderBot">');
  });

  it('supports stacked paths and literal equals patterns', async () => {
    const first = await request(app.getHttpServer()).get('/first/one').expect(200);
    const second = await request(app.getHttpServer()).get('/second/two').expect(200);
    const equals = await request(app.getHttpServer()).get('/m=Arrival').expect(200);

    expect(first.text).toContain('<title>Stacked one</title>');
    expect(second.text).toContain('<title>Stacked two</title>');
    expect(equals.text).toContain('<title>Arrival</title>');
  });

  it('falls back to defaults for null, undefined, and thrown results without returning 500', async () => {
    const warn = jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
    const empty = await request(app.getHttpServer()).get('/empty').expect(200);
    const undefinedResult = await request(app.getHttpServer()).get('/undefined').expect(200);
    const failure = await request(app.getHttpServer()).get('/failure').expect(200);

    expect(empty.text).toContain('<title>Default title</title>');
    expect(undefinedResult.text).toContain('<title>Default title</title>');
    expect(failure.text).toContain('<title>Default title</title>');
    expect(warn).toHaveBeenCalledWith(expect.stringContaining('lookup failed'));
    warn.mockRestore();
  });

  it('allows Nest guards to deny a render route', async () => {
    const response = await request(app.getHttpServer()).get('/denied').expect(403);
    expect(response.body.message).toBe('Forbidden resource');
    expect(response.text).not.toContain('Must not render');
  });

  it('runs application interceptors, filters, and request-scoped providers through Nest', async () => {
    const intercepted = await request(app.getHttpServer()).get('/intercepted').expect(200);
    expect(intercepted.text).toContain('<title>Intercepted route</title>');

    const filtered = await request(app.getHttpServer()).get('/filtered').expect(418);
    expect(filtered.body).toEqual({ filtered: 'filtered denial' });

    const first = await request(app.getHttpServer()).get('/request-scope').expect(200);
    const second = await request(app.getHttpServer()).get('/request-scope').expect(200);
    expect(first.text).toMatch(/<title>Request \d+<\/title>/);
    expect(second.text).toMatch(/<title>Request \d+<\/title>/);
    expect(first.text.match(/<title>(.*?)<\/title>/)?.[1])
      .not.toBe(second.text.match(/<title>(.*?)<\/title>/)?.[1]);
  });

  it('handles HEAD shell requests without a body', async () => {
    const response = await request(app.getHttpServer())
      .head('/deep/client/route')
      .set('accept', 'text/html')
      .expect(200)
      .expect('cache-control', 'no-cache');
    expect(response.text).toBeUndefined();
  });
});

@Module({
  imports: [
    GolemRenderModule.forRoot({
      client: CLIENT,
      reserved: ['/backend'],
    }),
  ],
})
class HostingOnlyModule {}

describe('custom hosting configuration (e2e)', () => {
  let app: INestApplication;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({ imports: [HostingOnlyModule] }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
  });

  afterAll(async () => {
    await app.close();
  });

  it('supports hosting without metadata and configurable reserved prefixes', async () => {
    const shell = await request(app.getHttpServer())
      .get('/api/is-now-a-client-route')
      .set('accept', 'text/html')
      .expect(200);
    expect(shell.text).toBe(readFileSync(INDEX, 'utf8'));

    await request(app.getHttpServer())
      .get('/backend/missing')
      .set('accept', 'text/html')
      .expect(404);
  });
});
