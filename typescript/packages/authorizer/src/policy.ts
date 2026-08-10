import {
  Ability,
  AbilityBuilder,
  AbilityOptions,
  AbilityTuple,
  ConditionsMatcher,
  RawRuleFrom,
  fieldPatternMatcher,
} from '@casl/ability';
import {
  ConditionIssue,
  SUPPORTED_COMBINATORS,
  SUPPORTED_OPERATORS,
  formatConditionIssues,
  policyConditionsMatcher,
  validateConditions,
} from '@eleven-am/golem-policy';
import { AbilityBuilderFactory, AbilityBuilderLike, CaslRule } from './casl';

export const UNSUPPORTED_POLICY_CONDITION_ERROR =
  'Golem authorization refuses a policy rule it cannot evaluate exactly';

function quoteList(value: unknown): string {
  if (value === undefined || value === null) {
    return '<unknown>';
  }
  const values = Array.isArray(value) ? (value as unknown[]) : [value];
  return values.map((entry) => `'${String(entry)}'`).join(', ');
}

export function describeRule(rule: CaslRule): string {
  const verb = rule.inverted ? 'cannot' : 'can';
  const fields = rule.fields === undefined ? '' : `, [${quoteList(rule.fields)}]`;
  return `${verb}(${quoteList(rule.action ?? rule.actions)}, ${quoteList(rule.subject)}${fields})`;
}

export class GolemPolicyRuleError extends Error {
  readonly rule: CaslRule;

  readonly issues: readonly ConditionIssue[];

  readonly operators: readonly string[];

  constructor(rule: CaslRule, issues: readonly ConditionIssue[]) {
    super(
      `${UNSUPPORTED_POLICY_CONDITION_ERROR}: rule ${describeRule(rule)} ${formatConditionIssues(issues)}. ` +
        `Supported operators are ${SUPPORTED_OPERATORS.join(', ')}; supported combinators are ${SUPPORTED_COMBINATORS.join(', ')}. ` +
        'Rewrite the rule with a supported operator, or enforce this restriction outside the policy.',
    );
    this.name = 'GolemPolicyRuleError';
    this.rule = rule;
    this.issues = issues;
    this.operators = [...new Set(issues.map((entry) => entry.operator ?? '<none>'))];
    Object.setPrototypeOf(this, GolemPolicyRuleError.prototype);
  }
}

export function assertRulesSupported(rules: readonly CaslRule[]): void {
  for (const rule of rules) {
    if (rule.conditions === undefined || rule.conditions === null) {
      continue;
    }
    const result = validateConditions(rule.conditions);
    if (result.supported) {
      continue;
    }
    throw new GolemPolicyRuleError(rule, result.issues);
  }
}

export function createGolemAbility<A extends AbilityTuple = [string, string], C = Record<string, unknown>>(
  rules: RawRuleFrom<A, C>[] = [],
  options: AbilityOptions<A, C> = {},
): Ability<A, C> {
  assertRulesSupported(rules as unknown as readonly CaslRule[]);
  return new Ability<A, C>(rules, {
    ...options,
    conditionsMatcher: policyConditionsMatcher as unknown as ConditionsMatcher<C>,
    fieldMatcher: fieldPatternMatcher,
  });
}

export function golemAbilityBuilderFactory(): AbilityBuilderLike {
  return new AbilityBuilder(createGolemAbility) as unknown as AbilityBuilderLike;
}

export function withRuleValidation(factory: AbilityBuilderFactory): AbilityBuilderFactory {
  return () => {
    const builder = factory();
    const build = builder.build.bind(builder);
    const validating: AbilityBuilderLike = Object.assign(builder, {
      build: (options?: unknown) => {
        assertRulesSupported(builder.rules);
        return build(options);
      },
    });
    return validating;
  };
}
