import { INestApplication } from '@nestjs/common';
import request from 'supertest';
import { bootDemoApp, shutdownDemoApp } from './harness';

describe('generated aggregation surface (e2e)', () => {
  let app: INestApplication;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  function query(source: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', 'token-roy@example.com')
      .send({ query: source });
  }

  it('groups without an explicit orderBy or take', async () => {
    const response = await query(`{
      postsGrouped(by: [published], measures: { count: true }) {
        key { published }
        count
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsGrouped).toHaveLength(2);
  });

  it('groups with take but no orderBy', async () => {
    const response = await query(`{
      postsGrouped(by: [published], measures: { count: true }, take: 3) {
        key { published }
        count
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsGrouped.length).toBeGreaterThan(0);
  });

  it('groups by an enum dimension', async () => {
    const response = await query(`{
      postsGrouped(by: [type], measures: { count: true }) {
        key { type }
        count
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    const groups = response.body.data.postsGrouped as {
      key: { type: string };
      count: number;
    }[];
    expect(groups.length).toBeGreaterThan(0);
    expect(groups.every((group) => typeof group.key.type === 'string')).toBe(true);
  });

  it('exposes the enum dimension with its own type in the schema', async () => {
    const response = await query(`{
      __type(name: "PostGroupKey") { fields { name type { name } } }
    }`);

    const fields = response.body.data.__type.fields as {
      name: string;
      type: { name: string | null };
    }[];
    const type = fields.find((entry) => entry.name === 'type');
    expect(type?.type.name).toBe('PostType');
  });

  it('refuses an explicit take beyond the configured cap, naming it as explicit', async () => {
    const response = await query(`{
      postsGrouped(by: [published], measures: { count: true }, take: 500) {
        count
      }
    }`);

    expect(response.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
    expect(response.body.errors[0].message).toMatch(/explicit take of at most 50/);
  });

  it('maps a rejected aggregate argument to a stable code without prisma internals', async () => {
    const response = await query(`{
      postsGrouped(by: [published], measures: { count: true }, skip: -5) {
        count
      }
    }`);

    const error = response.body.errors[0];
    expect(error.extensions.code).toBe('BAD_USER_INPUT');
    expect(error.message).not.toMatch(/dist\/operations\.js|delegate\.groupBy/);
  });

  it('still orders by a measure when asked', async () => {
    const response = await query(`{
      postsGrouped(
        by: [published]
        measures: { count: true, sum: [viewCount] }
        orderBy: { sum: { viewCount: desc } }
        take: 1
      ) { key { published } count }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsGrouped).toHaveLength(1);
    expect(response.body.data.postsGrouped[0].key.published).toBe(true);
  });
});
