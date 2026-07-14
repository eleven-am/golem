import { Injectable, UseGuards } from '@nestjs/common';
import { Args, Context } from '@nestjs/graphql';
import {
  ComputedField,
  CustomMutation,
  CustomQuery,
  GolemValidationError,
} from '@eleven-am/golem';
import { GolemPrismaService } from './generated/golem/client';
import { SearchPostsGuard } from './search-posts.guard';

@Injectable()
export class UserExtension {
  constructor(private readonly prisma: GolemPrismaService) {}

  @ComputedField('User', { type: 'String!', requires: ['name', 'email'] })
  displayName(parent: { name: string | null; email: string }): string {
    return parent.name ?? parent.email;
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
