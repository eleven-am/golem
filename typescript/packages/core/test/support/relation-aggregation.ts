import type { AuthorizationProvider } from '../../src/authorization';
import { buildRelationAggregationPlan } from '../../src/relation-aggregation';
import { GolemEngine } from '../../src/operations';
import { scopedModels } from './fixture';
import { context } from './analytics';

export interface RelationAggregationFixture {
  provider: 'sqlite' | 'postgresql';
  client: Record<string, any>;
}

const metric = scopedModels.find((model) => model.name === 'Metric')!;
const plan = buildRelationAggregationPlan(
  metric,
  {
    relationDimensions: {
      ownerTenant: { path: ['owner'], field: 'tenantId' },
    },
    measures: ['rank', 'score', 'hits'],
    maxIntermediateGroups: 100,
    maxGroups: 10,
  },
  new Map(scopedModels.map((model) => [model.name, model])),
)!;

const authorization: AuthorizationProvider = {
  authorize: async () => undefined,
  constrain: async (_action, model) =>
    model === 'User'
      ? { tenantId: 1 }
      : model === 'Metric'
        ? { id: { lte: 5 } }
        : undefined,
  checkField: async () => true,
  classifyFields: async (_action, _model, fields) =>
    Object.fromEntries(fields.map((field) => [field, { access: 'always' }])),
};

export function relationAggregationSuite(
  fixture: () => RelationAggregationFixture,
): void {
  it('executes relation-aware aggregation with exact provider values and policy at both phases', async () => {
    const { client, provider } = fixture();
    const engine = new GolemEngine(client, scopedModels, {
      provider,
      authorization,
      checkReadFields: true,
      checkWriteResults: false,
      relationAggregations: new Map([['Metric', plan]]),
    });

    const rows = await engine.relationGroupBy({
      model: 'Metric',
      by: ['ownerTenant'],
      having: { avg: { rank: { gt: 2 } } },
      orderBy: { key: { ownerTenant: 'desc' } },
      _count: true,
      _sum: { rank: true, score: true, hits: true },
      _avg: { rank: true, score: true },
      _min: { rank: true },
      _max: { rank: true },
      context,
    }) as Array<Record<string, any>>;

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      ownerTenant: 1,
      _count: 4,
      _sum: { rank: 7, hits: 42n },
      _avg: { rank: 7 / 3 },
      _min: { rank: 1 },
      _max: { rank: 4 },
    });
    expect(rows[0]!._sum.score.toString()).toBe(
      provider === 'postgresql'
        ? '1234567900.623456789012300001'
        : '1234567900.6234567',
    );
    const oracle = await client.metric.aggregate({
      where: {
        id: { lte: 5 },
        owner: { tenantId: 1 },
      },
      _avg: { score: true },
    });
    expect(rows[0]!._avg.score.toString()).toBe(oracle._avg.score.toString());
  });
}
