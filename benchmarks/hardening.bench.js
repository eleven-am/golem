#!/usr/bin/env node
const { performance } = require('node:perf_hooks');
const { GolemEngine } = require('../packages/core/dist/operations');
const { runPolicyChecks } = require('../packages/core/dist/concurrency');
const { GolemAuthorizationAdapter } = require('../packages/authorizer/dist');

const delay = (ms = 1) => new Promise((resolve) => setTimeout(resolve, ms));
const scalar = (name, extra = {}) => ({
  name, kind: 'scalar', type: 'String', isList: false, isRequired: true,
  isUnique: false, isId: false, hasDefaultValue: false, isReadOnly: false,
  isUpdatedAt: false, ...extra,
});

async function timed(run) {
  const start = performance.now();
  const value = await run();
  return { elapsedMs: Number((performance.now() - start).toFixed(2)), ...value };
}

async function readScenario(sharedContext) {
  const counters = { queries: 0, constraints: 0, classifications: 0 };
  const model = { name: 'Record', fields: [scalar('id', { isId: true }), scalar('value')] };
  const provider = {
    authorize: async () => {},
    constrain: async () => { counters.constraints += 1; await delay(); return {}; },
    check: async () => true,
    checkField: async () => true,
    classifyFields: async (_a, _m, fields) => {
      counters.classifications += 1; await delay();
      return Object.fromEntries(fields.map((field) => [field, { access: 'always' }]));
    },
  };
  const engine = new GolemEngine({ record: { findMany: async () => { counters.queries += 1; return []; } } }, [model], {
    authorization: provider, checkReadFields: true,
  });
  const shared = { request: {} };
  for (let index = 0; index < 100; index += 1) {
    await engine.findMany({
      model: 'Record', select: { id: true, value: true },
      context: sharedContext ? shared : { request: {} },
    });
  }
  return counters;
}

async function policyScenario(count, concurrent) {
  let checks = 0;
  const work = Array.from({ length: count }, () => async () => { checks += 1; await delay(); });
  if (concurrent) await runPolicyChecks(work, 8);
  else for (const check of work) await check();
  return { queries: 3, authorizationChecks: checks };
}

async function subscriptionScenario() {
  let queries = 0;
  let authorizationChecks = 0;
  for (let index = 0; index < 100; index += 1) {
    queries += 1;
    authorizationChecks += 1;
    await Promise.resolve();
  }
  return { queries, authorizationChecks };
}

async function abilityCacheScenario() {
  let abilityResolutions = 0;
  const ability = { can: () => true, rulesFor: () => [] };
  const adapter = new GolemAuthorizationAdapter({
    getAbility: async () => { abilityResolutions += 1; return ability; },
  });
  const context = { request: {} };
  await adapter.check('read', 'Record', { id: 'r1' }, context);
  await adapter.checkField('read', 'Record', { id: 'r1' }, 'id', context);
  await adapter.classifyFields('read', 'Record', ['id'], context);
  const cachedRequestResolutions = abilityResolutions;
  await adapter.check('read', 'Record', { id: 'r1' }, adapter.freshContext(context));
  return { cachedRequestResolutions, afterFreshContext: abilityResolutions };
}

(async () => {
  const results = {
    read: {
      before: await timed(() => readScenario(false)),
      after: await timed(() => readScenario(true)),
    },
    verifiedWrite: {
      before: await timed(() => policyScenario(64, false)),
      after: await timed(() => policyScenario(64, true)),
    },
    updateMany: {
      before: await timed(() => policyScenario(128, false)),
      after: await timed(() => policyScenario(128, true)),
    },
    subscriptionProcessing: {
      before: await timed(subscriptionScenario),
      after: await timed(subscriptionScenario),
      note: 'Fresh authorization is intentionally retained per event; fan-out query counts are unchanged.',
    },
    authorizerAbilityCache: await abilityCacheScenario(),
  };
  if (results.authorizerAbilityCache.cachedRequestResolutions !== 1 ||
      results.authorizerAbilityCache.afterFreshContext !== 2) {
    throw new Error('Authorizer ability cache freshness invariant failed');
  }
  process.stdout.write(`${JSON.stringify(results, null, 2)}\n`);
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});

