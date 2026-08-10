import {
  CallHandler,
  CanActivate,
  ExecutionContext,
  Injectable,
  Module,
  NestInterceptor,
  PipeTransform,
  Scope,
} from '@nestjs/common';
import { GqlExecutionContext } from '@nestjs/graphql';
import { map, Observable } from 'rxjs';

@Injectable()
export class SearchPostsPolicy {
  allows(context: unknown): boolean {
    const request = (context as { req?: { headers?: Record<string, string> } } | null)?.req;
    return request?.headers?.['x-deny-search'] !== 'true';
  }
}

@Injectable()
export class SearchPostsGuard implements CanActivate {
  constructor(private readonly policy: SearchPostsPolicy) {}

  canActivate(context: ExecutionContext): boolean {
    return this.policy.allows(GqlExecutionContext.create(context).getContext());
  }
}

let nextRequestOrdinal = 0;

@Injectable({ scope: Scope.REQUEST })
export class RequestOrdinal {
  readonly value = ++nextRequestOrdinal;
}

@Injectable()
export class UppercasePipe implements PipeTransform<string, string> {
  transform(value: string): string {
    return value.toUpperCase();
  }
}

@Injectable()
export class ComputedSuffixInterceptor implements NestInterceptor {
  intercept(_context: ExecutionContext, next: CallHandler): Observable<unknown> {
    return next.handle().pipe(map((value) => `${value}!`));
  }
}

@Module({
  providers: [
    SearchPostsGuard,
    SearchPostsPolicy,
    RequestOrdinal,
    UppercasePipe,
    ComputedSuffixInterceptor,
  ],
  exports: [
    SearchPostsGuard,
    SearchPostsPolicy,
    RequestOrdinal,
    UppercasePipe,
    ComputedSuffixInterceptor,
  ],
})
export class SearchPostsAccessModule {}
