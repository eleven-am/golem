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

  it('serializes BigInt aggregates exactly and uses Prisma native average semantics', async () => {
    const response = await query(`{
      postsAggregate(measures: {
        sum: [viewCount]
        avg: [viewCount]
        min: [viewCount]
        max: [viewCount]
      }) {
        sum { viewCount }
        avg { viewCount }
        min { viewCount }
        max { viewCount }
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsAggregate).toEqual({
      sum: { viewCount: '9007199254741098' },
      avg: { viewCount: 3002399751580366 },
      min: { viewCount: '5' },
      max: { viewCount: '9007199254740993' },
    });
  });

  it('serializes Decimal aggregates as exact strings, including nullable source rows', async () => {
    const response = await query(`{
      postsAggregate(measures: {
        sum: [rating]
        avg: [rating]
        min: [rating]
        max: [rating]
      }) {
        sum { rating }
        avg { rating }
        min { rating }
        max { rating }
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsAggregate).toEqual({
      sum: { rating: '0.30000000000000004' },
      avg: { rating: '0.15000000000000002' },
      min: { rating: '0.1' },
      max: { rating: '0.2' },
    });
  });

  it('uses operation-specific aggregate field types in the GraphQL schema', async () => {
    const response = await query(`{
      sum: __type(name: "PostSumValues") { fields { name type { name } } }
      avg: __type(name: "PostAvgValues") { fields { name type { name } } }
      min: __type(name: "PostMinValues") { fields { name type { name } } }
      max: __type(name: "PostMaxValues") { fields { name type { name } } }
    }`);

    const typeOf = (kind: 'sum' | 'avg' | 'min' | 'max', field: string) =>
      response.body.data[kind].fields.find((entry: { name: string }) => entry.name === field).type.name;
    expect(typeOf('sum', 'viewCount')).toBe('BigInt');
    expect(typeOf('avg', 'viewCount')).toBe('Float');
    expect(typeOf('min', 'viewCount')).toBe('BigInt');
    expect(typeOf('max', 'viewCount')).toBe('BigInt');
    expect(typeOf('sum', 'rating')).toBe('Decimal');
    expect(typeOf('avg', 'rating')).toBe('Decimal');
  });

  it('preserves exact BigInt and Decimal values in grouped results', async () => {
    const response = await query(`{
      postsGrouped(
        by: [published]
        measures: { sum: [viewCount, rating], min: [viewCount, rating], max: [viewCount, rating] }
      ) {
        key { published }
        sum { viewCount rating }
        min { viewCount rating }
        max { viewCount rating }
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    const published = response.body.data.postsGrouped.find(
      (group: { key: { published: boolean } }) => group.key.published,
    );
    expect(published).toEqual({
      key: { published: true },
      sum: { viewCount: '9007199254741093', rating: '0.30000000000000004' },
      min: { viewCount: '100', rating: '0.1' },
      max: { viewCount: '9007199254740993', rating: '0.2' },
    });
  });
});
