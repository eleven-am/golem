import type { ContextBoundClient } from '../src/generated/golem/client';
import type { GolemRequest } from '../src/generated/golem/types';

declare const scoped: ContextBoundClient;

async function contextClientContract(): Promise<void> {
  const selected = await scoped.post.findMany({ select: { title: true } });
  selected[0]?.title.toUpperCase();
  // @ts-expect-error select-narrowed results do not expose unselected fields
  selected[0]?.viewCount;

  const included = await scoped.post.findUnique({
    where: { id: 'post-1' },
    include: { author: true },
  });
  included?.author.email.toUpperCase();

  const total: number = await scoped.post.count({ where: { published: true } });
  void total;
  // @ts-expect-error selected count objects are not supported by the engine contract
  await scoped.post.count({ select: { authorId: true } });

  await scoped.post.updateMany({
    where: { published: false },
    data: { published: true },
    // @ts-expect-error verified updateMany cannot preserve Prisma limit semantics
    limit: 10,
  });
  await scoped.post.deleteMany({
    where: { published: false },
    // @ts-expect-error deleteMany limit is not part of the Golem operation contract
    limit: 10,
  });

  const aggregate = await scoped.post.aggregate({
    where: { published: true },
    orderBy: { createdAt: 'desc' },
    cursor: { id: 'post-1' },
    take: 10,
    skip: 1,
    _sum: { viewCount: true },
  });
  aggregate._sum.viewCount?.valueOf();

  const groups = await scoped.post.groupBy({
    by: ['authorId'],
    _sum: { viewCount: true },
  });
  groups[0]?.authorId.toUpperCase();
  groups[0]?._sum.viewCount?.valueOf();

  await scoped.$transaction(async (tx) => {
    const transactional = await tx.post.findMany({ select: { id: true } });
    transactional[0]?.id.toUpperCase();
    // @ts-expect-error transaction delegates use the same restricted batch contract
    await tx.post.deleteMany({ limit: 1 });
  });

  // @ts-expect-error raw-only Prisma operations are absent from the context-bound surface
  await scoped.post.createMany({ data: [] });
}

const hookRequest = {
  model: 'Post',
  include: { author: true },
} satisfies GolemRequest<'Post', 'findMany'>;

void contextClientContract;
void hookRequest;
