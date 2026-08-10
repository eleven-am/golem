import { INestApplication } from '@nestjs/common';
import {
  ADA,
  EfficiencyPersona,
  EfficiencyShape,
  GUEST,
  MOD,
  dialectFor,
  efficiencyPlays,
  efficiencyQuery,
  efficiencySuite,
} from '../../../packages/core/test/support/efficiency';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

const emails: Record<EfficiencyPersona, string> = {
  ada: 'ada@example.com',
  mod: 'mod@example.com',
  guest: 'guest@example.com',
  everyone: 'roy@example.com',
};

const owners: Record<number, string> = {
  [ADA]: emails.ada,
  [MOD]: emails.mod,
  [GUEST]: emails.guest,
};

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('a scoped efficiency metric over listening history (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  const ids = new Map<number, string>();

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    for (const [key, email] of Object.entries(owners)) {
      const user = await prisma.user.findUnique({ where: { email } });
      ids.set(Number(key), user!.id);
    }
    await prisma.play.createMany({
      data: efficiencyPlays.map((row) => ({
        id: row.id,
        userId: ids.get(row.userKey)!,
        ts: new Date(row.ts),
        msPlayed: row.msPlayed,
        reasonStart: row.reasonStart,
        reasonEnd: row.reasonEnd,
        trackUri: row.trackUri,
        trackName: row.trackName,
        artistName: row.artistName,
      })),
    });
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  efficiencySuite(() => ({
    provider: 'sqlite',
    userId: (key: number) => ids.get(key),
    run: async (persona: EfficiencyPersona, shape: EfficiencyShape) => {
      const rows = await prisma
        .forContext(ctxFor(emails[persona]))
        .$scoped('Play')
        .query(efficiencyQuery(shape, dialectFor('sqlite')) as never)
        .execute();
      return rows as Record<string, unknown>[];
    },
  }));
});
