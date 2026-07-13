import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import request from 'supertest';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';

describe('golem demo (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({
      imports: [AppModule],
    }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    prisma = app.get(GolemPrismaService);
    await seed(prisma);
  });

  afterAll(async () => {
    await app.close();
  });

  function gql(query: string) {
    return request(app.getHttpServer()).post('/graphql').set('authorization', 'token-roy@example.com').send({ query }).expect(200);
  }

  it('serves a nested findMany across one-to-many and one-to-one relations', async () => {
    const response = await gql(`{
      users(orderBy: [{ email: asc }]) {
        email
        name
        posts { title published }
        profile { bio }
      }
    }`);
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      {
        email: 'ada@example.com',
        name: 'Ada',
        posts: [{ title: 'Memory systems', published: true }],
        profile: { bio: 'countess of computing' },
      },
      {
        email: 'guest@example.com',
        name: 'Guest',
        posts: [],
        profile: null,
      },
      {
        email: 'mod@example.com',
        name: 'Mod',
        posts: [],
        profile: null,
      },
      {
        email: 'roy@example.com',
        name: 'Roy',
        posts: [
          { title: 'First post', published: true },
          { title: 'Draft post', published: false },
        ],
        profile: { bio: 'builder of things' },
      },
    ]);
  });

  it('filters and paginates through prisma arguments', async () => {
    const response = await gql(`{
      posts(where: { published: { equals: true } }, orderBy: [{ title: asc }], take: 1) {
        title
      }
    }`);
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.posts).toEqual([{ title: 'First post' }]);
  });

  it('resolves findOne through a unique field', async () => {
    const response = await gql(`{
      user(where: { email: "roy@example.com" }) {
        name
        profile { bio }
      }
    }`);
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.user).toEqual({
      name: 'Roy',
      profile: { bio: 'builder of things' },
    });
  });

  it('rejects unknown fields per the generated schema', async () => {
    const response = await request(app.getHttpServer())
      .post('/graphql')
      .send({ query: '{ users { password } }' });
    expect(response.body.errors).toBeDefined();
    expect(response.body.errors[0].message).toContain('Cannot query field "password"');
  });
});
