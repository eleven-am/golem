import { AbilityBuilder } from '@casl/ability';
import { createAbility } from '@eleven-am/authorizer/prisma';
import { AuthorizationService } from '@eleven-am/authorizer';
import { GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';

function freshCtx() {
  return { req: {} };
}

function adapterFor(
  define: (builder: AbilityBuilder<any>) => void,
): GolemAuthorizationAdapter {
  const builder = new AbilityBuilder(createAbility as any);
  define(builder);
  const ability = builder.build();
  const service = {
    getAbility: async () => ability,
    resolvedAbilityFactory: () => () => new AbilityBuilder(createAbility as any),
  } as unknown as AuthorizationService;
  return new GolemAuthorizationAdapter(service);
}

describe('classifyFields marks which conditions the query constraint discharges (e2e)', () => {
  it('discharges an owner-column scoped grant', async () => {
    const adapter = adapterFor((can) => {
      can.can('read', 'ReadingStat', { userId: 'user-1' });
    });

    const result = await adapter.classifyFields(
      'read',
      'ReadingStat',
      ['wordsRead', 'date'],
      freshCtx(),
    );

    expect(result.wordsRead).toMatchObject({
      access: 'conditional',
      dischargedByConstraint: true,
    });
    expect(result.date).toMatchObject({ dischargedByConstraint: true });
  });

  it('discharges a relation-scoped grant on a model with no owner column', async () => {
    const adapter = adapterFor((can) => {
      can.can('read', 'ReadingSession', { article: { is: { userId: 'user-1' } } });
    });

    const result = await adapter.classifyFields(
      'read',
      'ReadingSession',
      ['durationSec'],
      freshCtx(),
    );

    expect(result.durationSec).toMatchObject({
      access: 'conditional',
      dischargedByConstraint: true,
    });
  });

  it('does not discharge a field-level condition sitting beside a scoped grant', async () => {
    const adapter = adapterFor((can) => {
      can.can('read', 'M', { userId: 'user-1' });
      can.can('read', 'M', 'secretField', { flag: true });
    });

    const result = await adapter.classifyFields(
      'read',
      'M',
      ['normalField', 'secretField'],
      freshCtx(),
    );

    expect(result.normalField).toMatchObject({ dischargedByConstraint: true });
    expect(result.secretField).toMatchObject({
      access: 'conditional',
      dischargedByConstraint: false,
    });
    expect(result.secretField.requires).toContain('flag');
  });

  it('does not discharge an inverted conditional rule', async () => {
    const adapter = adapterFor((can) => {
      can.can('read', 'M', { userId: 'user-1' });
      can.cannot('read', 'M', { archived: true });
    });

    const result = await adapter.classifyFields('read', 'M', ['title'], freshCtx());

    expect(result.title).toMatchObject({ dischargedByConstraint: false });
  });

  it('leaves an unconditional grant classified always', async () => {
    const adapter = adapterFor((can) => {
      can.can('read', 'Voice');
    });

    const result = await adapter.classifyFields('read', 'Voice', ['name'], freshCtx());

    expect(result.name).toEqual({ access: 'always' });
  });
});
