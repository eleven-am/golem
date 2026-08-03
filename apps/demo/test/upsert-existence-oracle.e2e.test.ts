import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { AbilityBuilder } from '@casl/ability';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import {
  Authorizer,
  AuthorizationModule,
  ResolvedAbility,
  ResolvedUser,
  WillAuthorize,
} from '@eleven-am/authorizer';
import { GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';
import { GolemModule } from '@eleven-am/golem';
import { DemoAuthenticator } from '../src/auth';
import { getDatamodel } from '../src/generated/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { databaseFileFor, provisionDatabase, removeDatabaseFiles } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

type Outcome = { settled: 'answered' } | { settled: 'refused'; code: string };

async function outcome(run: () => Promise<unknown>): Promise<Outcome> {
  try {
    await run();
    return { settled: 'answered' };
  } catch (error) {
    return { settled: 'refused', code: (error as { code?: string }).code ?? 'UNKNOWN' };
  }
}

async function bootRules(suite: string, rules: unknown): Promise<{
  app: INestApplication;
  prisma: GolemPrismaService;
}> {
  const databaseFile = provisionDatabase(suite);
  const moduleRef = await Test.createTestingModule({
    providers: [rules as never],
    imports: [
      AuthorizationModule.forRootAsync({
        inject: [GolemPrismaService],
        useFactory: (client: GolemPrismaService) => new DemoAuthenticator(client),
      }),
      GolemModule.forRoot({
        client: GolemPrismaService,
        prismaOptions: { adapter: new PrismaBetterSqlite3({ url: `file:${databaseFile}` }) },
        datamodel: getDatamodel(),
        models: { PostTag: false, Play: false },
        authorization: GolemAuthorizationAdapter,
      }),
    ],
  }).compile();
  const app = moduleRef.createNestApplication();
  await app.init();
  const prisma = app.get(GolemPrismaService);
  await seed(prisma);
  return { app, prisma };
}

@Authorizer()
class UpdateWithoutCreateRules implements WillAuthorize {
  forUser(user: ResolvedUser, builder: AbilityBuilder<ResolvedAbility>): void {
    const { can } = builder;
    can('read', 'User');
    can('update', 'User', { id: (user as { id: string }).id });
  }
}

@Authorizer()
class CreateWithoutUpdateRules implements WillAuthorize {
  forUser(_user: ResolvedUser, builder: AbilityBuilder<ResolvedAbility>): void {
    const { can } = builder;
    can('read', 'User');
    can('create', 'User');
  }
}

@Authorizer()
class CreateAndUpdateRules implements WillAuthorize {
  forUser(_user: ResolvedUser, builder: AbilityBuilder<ResolvedAbility>): void {
    const { can } = builder;
    can('read', 'User');
    can('create', 'User');
    can('update', 'User');
  }
}

describe('an upsert by a caller who may not create (e2e)', () => {
  const suite = `${__filename}.no-create`;
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const booted = await bootRules(suite, UpdateWithoutCreateRules);
    app = booted.app;
    prisma = booted.prisma;
  });

  beforeEach(async () => {
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
    removeDatabaseFiles(databaseFileFor(suite));
  });

  function asCaller() {
    return prisma.forContext(ctxFor('ada@example.com'));
  }

  function upsertBy(email: string) {
    return asCaller().user.upsert({
      where: { email },
      create: { email, name: 'Probe' },
      update: { name: 'Probe' },
      select: { id: true },
    });
  }

  it('tells the caller nothing about an email held by a row it may not update', async () => {
    const hit = await outcome(() => upsertBy('roy@example.com'));
    const miss = await outcome(() => upsertBy('ghost@example.com'));

    expect(hit).toEqual(miss);
    const roy = await prisma.user.findUniqueOrThrow({ where: { email: 'roy@example.com' } });
    expect(roy.name).toBe('Roy');
    expect(await prisma.user.findUnique({ where: { email: 'ghost@example.com' } })).toBeNull();
  });

  it('tells the caller nothing about an email through a compound-free unique selector', async () => {
    const hit = await outcome(() => upsertBy('mod@example.com'));
    const miss = await outcome(() => upsertBy('nobody@example.com'));

    expect(hit).toEqual(miss);
    expect(hit).toEqual(await outcome(() => asCaller().user.create({ data: { email: 'x@example.com' } })));
  });

  it('still upserts the row the caller may update', async () => {
    const updated = await asCaller().user.upsert({
      where: { email: 'ada@example.com' },
      create: { email: 'ada@example.com', name: 'Fresh' },
      update: { name: 'Renamed' },
      select: { email: true, name: true },
    });

    expect(updated).toEqual({ email: 'ada@example.com', name: 'Renamed' });
  });
});

describe('an upsert by a caller who may not update (e2e)', () => {
  const suite = `${__filename}.no-update`;
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const booted = await bootRules(suite, CreateWithoutUpdateRules);
    app = booted.app;
    prisma = booted.prisma;
  });

  beforeEach(async () => {
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
    removeDatabaseFiles(databaseFileFor(suite));
  });

  function asCaller() {
    return prisma.forContext(ctxFor('ada@example.com'));
  }

  it('answers a taken email exactly as a direct create answers it', async () => {
    const viaUpsert = await outcome(() =>
      asCaller().user.upsert({
        where: { email: 'roy@example.com' },
        create: { email: 'roy@example.com', name: 'Probe' },
        update: { name: 'Probe' },
        select: { id: true },
      }),
    );
    const viaCreate = await outcome(() =>
      asCaller().user.create({ data: { email: 'roy@example.com', name: 'Probe' }, select: { id: true } }),
    );

    expect(viaUpsert).toEqual(viaCreate);
    expect(viaUpsert).toEqual({ settled: 'refused', code: 'CONFLICT' });
  });

  it('answers a free email exactly as a direct create answers it', async () => {
    const viaUpsert = await outcome(() =>
      asCaller().user.upsert({
        where: { email: 'fresh-one@example.com' },
        create: { email: 'fresh-one@example.com', name: 'Probe' },
        update: { name: 'Probe' },
        select: { id: true },
      }),
    );
    const viaCreate = await outcome(() =>
      asCaller().user.create({ data: { email: 'fresh-two@example.com', name: 'Probe' }, select: { id: true } }),
    );

    expect(viaUpsert).toEqual(viaCreate);
    expect(viaUpsert).toEqual({ settled: 'answered' });
    expect(await prisma.user.findUnique({ where: { email: 'fresh-one@example.com' } })).not.toBeNull();
  });
});

describe('an upsert by a caller who may create and update (e2e)', () => {
  const suite = `${__filename}.both`;
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const booted = await bootRules(suite, CreateAndUpdateRules);
    app = booted.app;
    prisma = booted.prisma;
  });

  beforeEach(async () => {
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
    removeDatabaseFiles(databaseFileFor(suite));
  });

  function asCaller() {
    return prisma.forContext(ctxFor('ada@example.com'));
  }

  it('creates the row on the first upsert and updates it on the second', async () => {
    const created = await asCaller().user.upsert({
      where: { email: 'both@example.com' },
      create: { email: 'both@example.com', name: 'First' },
      update: { name: 'Second' },
      select: { id: true, name: true },
    });
    const updated = await asCaller().user.upsert({
      where: { email: 'both@example.com' },
      create: { email: 'both@example.com', name: 'First' },
      update: { name: 'Second' },
      select: { id: true, name: true },
    });

    expect(created.name).toBe('First');
    expect(updated).toEqual({ id: created.id, name: 'Second' });
    expect(await prisma.user.count({ where: { email: 'both@example.com' } })).toBe(1);
  });

  it('updates a row it did not author through the update branch', async () => {
    const updated = await asCaller().user.upsert({
      where: { email: 'roy@example.com' },
      create: { email: 'roy@example.com', name: 'Fresh' },
      update: { name: 'Edited' },
      select: { email: true, name: true },
    });

    expect(updated).toEqual({ email: 'roy@example.com', name: 'Edited' });
  });
});
