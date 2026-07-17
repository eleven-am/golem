import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { PrismaClient } from './generated/prisma/client';

type SeedClient = Record<'user' | 'post' | 'profile' | 'tag' | 'postTag', any>;

export async function seed(client: SeedClient): Promise<void> {
  await client.postTag.deleteMany();
  await client.post.deleteMany();
  await client.profile.deleteMany();
  await client.tag.deleteMany();
  await client.user.deleteMany();
  for (const label of ['alpha', 'beta', 'gamma', 'delta', 'epsilon']) {
    await client.tag.create({ data: { label } });
  }
  await client.user.create({
    data: {
      email: 'roy@example.com',
      name: 'Roy',
      phone: '+1-555-0100',
      profile: { create: { bio: 'builder of things' } },
      posts: {
        create: [
          { title: 'First post', published: true, viewCount: 9007199254740993n },
          { title: 'Draft post', published: false, viewCount: 5n },
        ],
      },
    },
  });
  await client.user.create({
    data: {
      email: 'ada@example.com',
      name: 'Ada',
      phone: '+44-555-0200',
      profile: { create: { bio: 'countess of computing' } },
      posts: { create: [{ title: 'Memory systems', published: true, viewCount: 100n }] },
    },
  });
  await client.user.create({
    data: {
      email: 'guest@example.com',
      name: 'Guest',
    },
  });
  await client.user.create({
    data: {
      email: 'mod@example.com',
      name: 'Mod',
    },
  });
}

if (require.main === module) {
  const client = new PrismaClient({ adapter: new PrismaBetterSqlite3({ url: 'file:./prisma/dev.db' }) });
  void seed(client)
    .then(() => client.$disconnect())
    .catch((error) => {
      process.exitCode = 1;
      return client.$disconnect().then(() => {
        throw error;
      });
    });
}
