import { existsSync, readFileSync, statSync } from 'node:fs';
import { extname, relative, resolve, sep } from 'node:path';
import { Inject, Injectable, OnModuleInit } from '@nestjs/common';
import { HttpAdapterHost } from '@nestjs/core';
import { static as serveStatic } from 'express';
import type { NextFunction, Request, RequestHandler, Response } from 'express';
import { HtmlShell } from './shell';
import {
  GOLEM_RENDER_OPTIONS,
  type GolemRenderOptions,
  type MetaTags,
} from './types';

const DEFAULT_RESERVED = ['/api', '/graphql'] as const;
const IMMUTABLE_CACHE = 'public, max-age=31536000, immutable';
const SHELL_CACHE = 'no-cache';

function normalizeReserved(path: string): string {
  const rooted = path.startsWith('/') ? path : `/${path}`;
  return rooted.length > 1 ? rooted.replace(/\/+$/, '') : rooted;
}

function isReservedPath(pathname: string, reserved: readonly string[]): boolean {
  return reserved.some((path) => pathname === path || pathname.startsWith(`${path}/`));
}

const ASSET_EXTENSIONS = new Set([
  '.js', '.mjs', '.cjs', '.css', '.map', '.json', '.wasm', '.webmanifest',
  '.png', '.jpg', '.jpeg', '.gif', '.svg', '.webp', '.avif', '.ico', '.bmp',
  '.woff', '.woff2', '.ttf', '.otf', '.eot',
  '.mp4', '.webm', '.ogg', '.mp3', '.wav', '.pdf', '.txt', '.xml',
]);

const SUBRESOURCE_DESTINATIONS = new Set([
  'script', 'style', 'image', 'font', 'audio', 'video', 'track', 'manifest',
  'worker', 'sharedworker', 'serviceworker', 'embed', 'object', 'xslt',
]);

function isAssetRequest(pathname: string, destination?: string): boolean {
  if (destination !== undefined && SUBRESOURCE_DESTINATIONS.has(destination)) {
    return true;
  }
  if (destination === 'document') {
    return false;
  }
  return (
    pathname === '/assets' ||
    pathname.startsWith('/assets/') ||
    pathname.split('/').some((segment) => segment.startsWith('.')) ||
    ASSET_EXTENSIONS.has(extname(pathname).toLowerCase())
  );
}

function destinationOf(request: Request): string | undefined {
  const header = request.headers['sec-fetch-dest'];
  return typeof header === 'string' ? header : undefined;
}

function isContentHashed(filePath: string): boolean {
  const name = filePath.split(/[\\/]/).at(-1) ?? '';
  const extensionAt = name.lastIndexOf('.');
  const stem = extensionAt < 0 ? name : name.slice(0, extensionAt);
  const token = stem.split(/[-.]/).at(-1) ?? '';
  return (
    token !== stem &&
    token.length >= 8 &&
    /^[A-Za-z0-9_]+$/.test(token) &&
    (/^[a-f0-9]+$/i.test(token) || /[A-Z0-9_]/.test(token))
  );
}

@Injectable()
export class GolemRenderService implements OnModuleInit {
  readonly clientDirectory: string;
  readonly indexPath: string;
  private readonly indexRequestPath: string;
  private readonly reserved: readonly string[];
  private readonly shell: HtmlShell;

  constructor(
    @Inject(GOLEM_RENDER_OPTIONS) options: GolemRenderOptions,
    private readonly adapterHost: HttpAdapterHost,
  ) {
    this.clientDirectory = resolve(options.client);
    this.indexPath = resolve(this.clientDirectory, options.index ?? 'index.html');
    const indexRelative = relative(this.clientDirectory, this.indexPath);
    if (indexRelative.startsWith('..') || resolve(this.indexPath) === this.clientDirectory) {
      throw new Error(`Frontend index must be a file inside ${this.clientDirectory}`);
    }
    if (!existsSync(this.indexPath) || !statSync(this.indexPath).isFile()) {
      throw new Error(`Frontend bundle is missing index file at ${this.indexPath}`);
    }
    this.indexRequestPath = `/${indexRelative.split(sep).join('/')}`;
    if (options.baseUrl) {
      try {
        new URL(options.baseUrl);
      } catch {
        throw new Error(`Invalid Golem render baseUrl: ${options.baseUrl}`);
      }
    }
    this.reserved = (options.reserved ?? DEFAULT_RESERVED).map(normalizeReserved);
    this.shell = new HtmlShell(
      readFileSync(this.indexPath, 'utf8'),
      options.defaults,
      options.baseUrl,
    );
  }

  onModuleInit(): void {
    const adapter = this.adapterHost.httpAdapter;
    if (adapter.getType() !== 'express') {
      throw new Error('@eleven-am/golem-render currently requires the Nest Express adapter');
    }
    const application = adapter.getInstance() as {
      use(...handlers: RequestHandler[]): void;
    };
    application.use(this.staticMiddleware(), this.spaFallback());
  }

  render(metadata: MetaTags | null | undefined, requestUrl: string): string {
    return this.shell.render(metadata, requestUrl);
  }

  send(
    response: Response,
    metadata: MetaTags | null | undefined,
    requestUrl: string,
  ): void {
    response.setHeader('Cache-Control', SHELL_CACHE);
    response.type('html').send(this.render(metadata, requestUrl));
  }

  private staticCacheHeaders(response: Response, filePath: string): void {
    response.setHeader(
      'Cache-Control',
      isContentHashed(filePath) ? IMMUTABLE_CACHE : SHELL_CACHE,
    );
  }

  private staticMiddleware(): RequestHandler {
    const unguarded = serveStatic(this.clientDirectory, {
      dotfiles: 'ignore',
      fallthrough: true,
      index: false,
      redirect: false,
      setHeaders: (response, filePath) => this.staticCacheHeaders(response, filePath),
    });
    return (request, response, next): void => {
      if (
        isReservedPath(request.path, this.reserved) ||
        request.path === this.indexRequestPath
      ) {
        next();
        return;
      }
      unguarded(request, response, next);
    };
  }

  private spaFallback(): RequestHandler {
    return (request: Request, response: Response, next: NextFunction): void => {
      if (
        (request.method !== 'GET' && request.method !== 'HEAD') ||
        isReservedPath(request.path, this.reserved) ||
        (request.path !== this.indexRequestPath &&
          isAssetRequest(request.path, destinationOf(request))) ||
        !request.accepts('html')
      ) {
        next();
        return;
      }
      this.send(response, undefined, request.originalUrl || request.url);
    };
  }
}
