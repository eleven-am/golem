import { Injectable } from '@nestjs/common';
import { ComputedField, CustomQuery } from '@eleven-am/golem';
import { GolemPrismaService } from './generated/golem/client';

@Injectable()
export class UserExtension {
  constructor(private readonly prisma: GolemPrismaService) {}

  @ComputedField('User', { type: 'String!', requires: ['name', 'email'] })
  displayName(parent: { name: string | null; email: string }): string {
    return parent.name ?? parent.email;
  }

  @CustomQuery({ type: '[Post!]!', args: { term: 'String!' } })
  searchPosts(args: { term: string }, ctx: unknown) {
    return this.prisma.forContext(ctx).post.findMany({
      where: { title: { contains: args.term } },
      select: { id: true, title: true, published: true },
    });
  }
}
