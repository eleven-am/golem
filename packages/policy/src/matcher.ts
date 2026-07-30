import type { ConditionsMatcher, MatchConditions } from '@casl/ability';
import { compileConditions } from './compile';
import { PolicyConditions } from './types';

export function createPolicyConditionsMatcher(): ConditionsMatcher<PolicyConditions> {
  return (conditions: PolicyConditions): MatchConditions => {
    const compiled = compileConditions(conditions);
    return (object: Record<PropertyKey, unknown>) => compiled.test(object);
  };
}

export const policyConditionsMatcher: ConditionsMatcher<PolicyConditions> = createPolicyConditionsMatcher();
