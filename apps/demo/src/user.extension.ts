import { Injectable, UseGuards, UseInterceptors } from '@nestjs/common';
import { Args, Context, Parent } from '@nestjs/graphql';
import {
  ComputedField,
  CustomMutation,
  CustomQuery,
  GolemValidationError,
} from '@eleven-am/golem';
import { GolemPrismaService } from './generated/golem/client';
import {
  ComputedSuffixInterceptor,
  RequestOrdinal,
  SearchPostsGuard,
  UppercasePipe,
} from './search-posts.guard';

class BaseUserExtension {
  @ComputedField('User', { type: 'String!', requires: ['email'] })
  emailDomain(@Parent() parent: { email: string }): string {
    return parent.email.split('@')[1];
  }
}

@Injectable()
export class UserExtension extends BaseUserExtension {
  constructor(
    private readonly prisma: GolemPrismaService,
    private readonly requestOrdinal: RequestOrdinal,
  ) {
    super();
  }

  @UseGuards(SearchPostsGuard)
  @ComputedField('User', { type: 'String!', requires: ['name', 'email'] })
  displayName(@Parent() parent: { name: string | null; email: string }): string {
    return parent.name ?? parent.email;
  }

  @ComputedField('User', { type: 'String!', requires: ['id'] })
  requestAuthorization(@Context() ctx: { req: { headers: Record<string, string> } }): string {
    return ctx.req.headers.authorization;
  }

  @ComputedField('User', {
    type: 'String!',
    requires: ['name', 'email'],
    args: { prefix: 'String!' },
  })
  greeting(
    @Parent() parent: { name: string | null; email: string },
    @Args('prefix', UppercasePipe) prefix: string,
  ): string {
    return `${prefix} ${parent.name ?? parent.email}`;
  }

  @UseInterceptors(ComputedSuffixInterceptor)
  @ComputedField('User', { type: 'String!', requires: ['name', 'email'] })
  interceptedName(@Parent() parent: { name: string | null; email: string }): string {
    return parent.name ?? parent.email;
  }

  @ComputedField('User', { type: 'Int!', requires: ['id'] })
  requestScope(): number {
    return this.requestOrdinal.value;
  }

  @ComputedField('User', { type: 'String!', requires: ['id'] })
  rejectComputedField(): never {
    throw new GolemValidationError('computed field rejected');
  }

  @UseGuards(SearchPostsGuard)
  @CustomQuery({ type: '[Post!]!', args: { term: 'String!' } })
  searchPosts(@Args() args: { term: string }, @Context() ctx: unknown) {
    return this.prisma.forContext(ctx).post.findMany({
      where: { title: { contains: args.term } },
      select: { id: true, title: true, published: true },
    });
  }

  @CustomMutation({ type: 'Boolean!' })
  rejectCustomOperation(): never {
    throw new GolemValidationError('custom operation rejected');
  }
}
