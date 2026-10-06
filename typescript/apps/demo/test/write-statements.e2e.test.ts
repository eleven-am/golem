import { INestApplication } from '@nestjs/common';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

const ADMIN = 'roy@example.com';

describe('the statements a write issues through the generated client (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let authorId: string;
  let postId: string;
  const statements: string[] = [];

  beforeAll(async () => {
    const context = await bootDemoApp(__filename, { golem: { onStatement: (sql) => statements.push(sql) } });
    app = context.app;
    prisma = context.prisma;
    authorId = (await prisma.user.findUniqueOrThrow({ where: { email: 'ada@example.com' } })).id;
    postId = (await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } })).id;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  const issued = async (write: () => Promise<unknown>): Promise<string[]> => {
    statements.length = 0;
    await write();
    return statements.splice(0);
  };
  const asAdmin = () => prisma.forContext({ req: { headers: { authorization: `token-${ADMIN}` } } });
  const reads = (sql: readonly string[], table: string) =>
    sql.filter((statement) => statement.startsWith(`SELECT \`main\`.\`${table}\`.`));

  it('reads a linked row once before and once after the write, and nothing more', async () => {
    const sql = await issued(() => asAdmin().comment.create({
      data: { body: 'hi', authorId, postId }, select: { id: true },
    }));

    expect(reads(sql, 'Post')).toHaveLength(2);
    expect(sql).toHaveLength(6);
  });

  it('updates a row without a second guard reading it', async () => {
    const sql = await issued(() => asAdmin().post.update({
      where: { id: postId }, data: { title: 'Memory systems' }, select: { id: true },
    }));

    expect(reads(sql, 'Post')).toHaveLength(0);
    expect(sql).toHaveLength(4);
  });
});
