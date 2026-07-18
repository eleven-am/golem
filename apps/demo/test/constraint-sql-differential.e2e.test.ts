import { INestApplication } from '@nestjs/common';
import { compileConstraint, quoteIdentifier } from '@eleven-am/golem-core';
import { GolemPrismaService } from '../src/generated/golem/client';
import { getDatamodel } from '../src/generated/golem';
import { bootDemoApp, shutdownDemoApp } from './harness';

const DIALECT = 'sqlite' as const;

function modelOrThrow(name: string) {
  const model = getDatamodel().models.find((entry) => entry.name === name);
  if (!model) {
    throw new Error(`missing model ${name}`);
  }
  return model;
}

describe('compiled constraint matches the prisma path (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  async function idsViaPrisma(where: Record<string, unknown>) {
    const rows = await prisma.post.findMany({
      where,
      select: { id: true },
      orderBy: { id: 'asc' },
    });
    return rows.map((row) => row.id);
  }

  async function idsViaCompiledSql(constraint: unknown) {
    const model = modelOrThrow('Post');
    const compiled = compileConstraint(constraint, { model, dialect: DIALECT });
    const table = quoteIdentifier(model.dbName ?? model.name, DIALECT);
    const idColumn = quoteIdentifier('id', DIALECT);
    const rows = (await prisma.$queryRawUnsafe(
      `SELECT ${idColumn} AS id FROM ${table} WHERE ${compiled.sql} ORDER BY ${idColumn} ASC`,
      ...compiled.params,
    )) as { id: string }[];
    return rows.map((row) => row.id);
  }

  async function expectAgreement(constraint: Record<string, unknown>) {
    const [viaPrisma, viaSql] = await Promise.all([
      idsViaPrisma(constraint),
      idsViaCompiledSql(constraint),
    ]);
    expect(viaSql).toEqual(viaPrisma);
    return viaPrisma;
  }

  it('agrees on an unconstrained read', async () => {
    const rows = await expectAgreement({});
    expect(rows.length).toBeGreaterThan(0);
  });

  it('agrees on scalar equality, the shape every app actually uses', async () => {
    const author = await prisma.user.findFirstOrThrow({ select: { id: true } });
    const rows = await expectAgreement({ authorId: author.id });
    expect(rows.length).toBeGreaterThan(0);
  });

  it('agrees on a boolean equality', async () => {
    await expectAgreement({ published: true });
    await expectAgreement({ published: false });
  });

  it('agrees on an in filter', async () => {
    const authors = await prisma.user.findMany({ select: { id: true } });
    const rows = await expectAgreement({
      authorId: { in: authors.map((entry) => entry.id) },
    });
    expect(rows.length).toBeGreaterThan(0);
  });

  it('agrees that an empty in filter selects nothing', async () => {
    const rows = await expectAgreement({ authorId: { in: [] } });
    expect(rows).toEqual([]);
  });

  it('agrees on an AND composition', async () => {
    const author = await prisma.user.findFirstOrThrow({ select: { id: true } });
    await expectAgreement({
      AND: [{ authorId: author.id }, { published: true }],
    });
  });

  it('refuses the shapes it cannot prove rather than selecting the wrong rows', () => {
    const model = modelOrThrow('Post');
    const refused: unknown[] = [
      { OR: [{ published: true }, { published: false }] },
      { NOT: { published: true } },
      { author: { is: { email: 'roy@example.com' } } },
      { title: { contains: 'a' } },
    ];
    for (const constraint of refused) {
      expect(() =>
        compileConstraint(constraint, { model, dialect: DIALECT }),
      ).toThrow(/Cannot compile the read constraint/);
    }
  });
});
