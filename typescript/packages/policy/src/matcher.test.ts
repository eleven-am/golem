import { Ability, AbilityBuilder, subject } from '@casl/ability';
import { UnsupportedConditionError } from './errors';
import { policyConditionsMatcher } from './matcher';

type Rules = Ability<[string, string | Record<PropertyKey, unknown>]>;

function abilityFor(declare: (can: AbilityBuilder<Rules>['can']) => void): Rules {
  const builder = new AbilityBuilder<Rules>(Ability);
  declare(builder.can);
  return builder.build({ conditionsMatcher: policyConditionsMatcher });
}

describe('casl integration', () => {
  it('matches a bigint rule condition against a numeric value', () => {
    const ability = abilityFor((can) => can('read', 'GolemBigIntProbe', { v: { equals: 1n } }));
    expect(ability.can('read', subject('GolemBigIntProbe', { v: 1 }))).toBe(true);
  });

  it('matches the demo analyst rule', () => {
    const ability = abilityFor((can) => can('read', 'Post', { viewCount: { gte: 100 } }));
    expect(ability.can('read', subject('Post', { viewCount: 100 }))).toBe(true);
    expect(ability.can('read', subject('Post', { viewCount: 99 }))).toBe(false);
    expect(ability.can('read', subject('Post', { viewCount: null }))).toBe(false);
  });

  it('matches the demo bigint rule past the safe integer boundary', () => {
    const ability = abilityFor((can) => can('update', 'Post', { viewCount: { gt: 9007199254740992 } }));
    expect(ability.can('update', subject('Post', { viewCount: 9007199254740993n }))).toBe(true);
    expect(ability.can('update', subject('Post', { viewCount: 9007199254740992n }))).toBe(false);
  });

  it('matches a one-hop relation rule', () => {
    const ability = abilityFor((can) =>
      can('read', 'ReadingSession', { post: { is: { authorId: 'u1' } } }),
    );
    expect(ability.can('read', subject('ReadingSession', { post: { authorId: 'u1' } }))).toBe(true);
    expect(ability.can('read', subject('ReadingSession', { post: { authorId: 'u2' } }))).toBe(false);
    expect(ability.can('read', subject('ReadingSession', { post: null }))).toBe(false);
  });

  it('refuses an unsupported rule condition instead of matching it', () => {
    const ability = abilityFor((can) => can('read', 'Post', { title: { search: 'x' } }));
    expect(() => ability.can('read', subject('Post', { title: 'x' }))).toThrow(UnsupportedConditionError);
  });
});
