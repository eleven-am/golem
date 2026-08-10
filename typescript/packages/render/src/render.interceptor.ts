import {
  CallHandler,
  ExecutionContext,
  Injectable,
  Logger,
  NestInterceptor,
} from '@nestjs/common';
import type { Request, Response } from 'express';
import { Observable, catchError, map, of } from 'rxjs';
import { GolemRenderService } from './render.service';
import type { MetaTags } from './types';

@Injectable()
export class RenderInterceptor implements NestInterceptor<unknown, undefined> {
  private readonly logger = new Logger(RenderInterceptor.name);

  constructor(private readonly renderer: GolemRenderService) {}

  intercept(context: ExecutionContext, next: CallHandler): Observable<undefined> {
    const request = context.switchToHttp().getRequest<Request>();
    const response = context.switchToHttp().getResponse<Response>();
    const render = (metadata: MetaTags | null | undefined): undefined => {
      if (!response.headersSent) {
        this.renderer.send(response, metadata, request.originalUrl || request.url);
      }
      return undefined;
    };

    return next.handle().pipe(
      map((metadata) => render(metadata as MetaTags | null | undefined)),
      catchError((error: unknown) => {
        const detail = error instanceof Error ? error.stack ?? error.message : String(error);
        this.logger.warn(`Render route failed; using defaults\n${detail}`);
        render(undefined);
        return of(undefined);
      }),
    );
  }
}
