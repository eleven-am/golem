import { Injectable } from '@nestjs/common';
import { Context, Parent } from '@nestjs/graphql';
import { BatchedComputedField, ComputedField } from '@eleven-am/golem';
import { GolemPrismaService } from './generated/golem/client';

@Injectable()
export class UserCountsExtension {
  constructor(private readonly prisma: GolemPrismaService) {}

  @ComputedField('User', { type: 'Int!', requires: ['id'] })
  postCountPerRow(@Parent() parent: { id: string }, @Context() ctx: unknown): Promise<number> {
    return this.prisma.forContext(ctx).post.count({ where: { authorId: parent.id } });
  }

  @BatchedComputedField('User', { type: 'Int!', key: 'id' })
  postCount(keys: readonly string[], ctx: unknown): Promise<Map<string, number>> {
    return this.countPosts(keys, ctx, {});
  }

  @BatchedComputedField('User', {
    type: 'Int',
    key: 'id',
    args: { published: 'Boolean!' },
  })
  postCountWhere(
    keys: readonly string[],
    ctx: unknown,
    args: { published: boolean },
  ): Promise<Map<string, number>> {
    return this.countPosts(keys, ctx, { published: args.published });
  }

  private async countPosts(
    keys: readonly string[],
    ctx: unknown,
    where: { published?: boolean },
  ): Promise<Map<string, number>> {
    const groups = await this.prisma.forContext(ctx).post.groupBy({
      by: ['authorId'],
      where: { ...where, authorId: { in: [...keys] } },
      _count: true,
    });
    const counts = new Map(keys.map((key) => [key, 0]));
    for (const group of groups) {
      counts.set(group.authorId, group._count);
    }
    return counts;
  }
}
