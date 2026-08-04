export interface CaslRule {
  action?: unknown;
  actions?: unknown;
  subject?: unknown;
  fields?: string | string[];
  conditions?: unknown;
  inverted?: boolean;
}

export interface AbilityLike {
  can(action: string, subject: unknown, field?: string): boolean;
  rulesFor(action: string, subject: string, field?: string): readonly CaslRule[];
}

export interface AbilityBuilderLike {
  rules: readonly CaslRule[];
  can(action: string, subject: string, conditions?: unknown): unknown;
  build(options?: unknown): AbilityLike;
}

export type AbilityBuilderFactory = () => AbilityBuilderLike;
