import { CanActivate, ExecutionContext, Injectable, Module } from '@nestjs/common';
import { GqlExecutionContext } from '@nestjs/graphql';

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

@Module({
  providers: [SearchPostsGuard, SearchPostsPolicy],
  exports: [SearchPostsGuard, SearchPostsPolicy],
})
export class SearchPostsAccessModule {}
