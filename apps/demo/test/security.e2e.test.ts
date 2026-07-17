import { INestApplication } from '@nestjs/common';
import request from 'supertest';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

describe('nested constraints and depth limits (e2e)', () => {
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

  function gql(query: string, token: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', token)
      .send({ query });
  }

  it('filters to-many relation reads by the caller ability', async () => {
    const query = '{ users(orderBy: [{ email: asc }]) { email posts { title } } }';

    const asGuest = await gql(query, 'token-guest@example.com');
    expect(asGuest.body.errors).toBeUndefined();
    const guestTitles = asGuest.body.data.users.flatMap((u: { posts: { title: string }[] }) =>
      u.posts.map((p) => p.title),
    );
    expect(guestTitles).toContain('First post');
    expect(guestTitles).toContain('Memory systems');
    expect(guestTitles).not.toContain('Draft post');

    const asRoy = await gql(query, 'token-roy@example.com');
    const royTitles = asRoy.body.data.users.flatMap((u: { posts: { title: string }[] }) =>
      u.posts.map((p) => p.title),
    );
    expect(royTitles).toContain('Draft post');
  });

  it('nulls to-one relations the caller ability denies', async () => {
    const response = await gql(
      '{ users(orderBy: [{ email: asc }]) { email profile { bio } } }',
      'token-ada@example.com',
    );
    expect(response.body.errors).toBeUndefined();
    const byEmail = Object.fromEntries(
      response.body.data.users.map((u: { email: string; profile: unknown }) => [u.email, u.profile]),
    );
    expect(byEmail['ada@example.com']).toEqual({ bio: 'countess of computing' });
    expect(byEmail['roy@example.com']).toBeNull();
  });

  it('rejects queries beyond the depth limit', async () => {
    const response = await gql(
      '{ users { posts { author { posts { author { posts { title } } } } } } }',
      'token-roy@example.com',
    );
    expect(response.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
    expect(response.body.errors[0].message).toContain('depth 6 exceeds the maximum of 5');
  });

  it('keeps depth-limit-compliant queries working', async () => {
    const response = await gql(
      '{ users { posts { author { posts { author { email } } } } } }',
      'token-roy@example.com',
    );
    expect(response.body.errors).toBeUndefined();
  });
});
