export type ConditionIssueReason = 'unsupported-operator' | 'unsupported-value' | 'unsupported-shape';

export interface ConditionIssue {
  readonly reason: ConditionIssueReason;
  readonly operator: string | null;
  readonly path: readonly string[];
  readonly message: string;
}

export type ConditionValidationResult =
  | { readonly supported: true; readonly issues: readonly [] }
  | { readonly supported: false; readonly issues: readonly [ConditionIssue, ...ConditionIssue[]] };

export const UNSUPPORTED_CONDITION_ERROR_NAME = 'UnsupportedConditionError';

export function formatConditionPath(path: readonly string[]): string {
  return path.length === 0 ? '<root>' : path.join('.');
}

export function formatConditionIssue(issue: ConditionIssue): string {
  return `${issue.message} (at ${formatConditionPath(issue.path)})`;
}

export function formatConditionIssues(issues: readonly ConditionIssue[]): string {
  return issues.map(formatConditionIssue).join('; ');
}

export class UnsupportedConditionError extends Error {
  readonly issues: readonly ConditionIssue[];

  readonly conditions: unknown;

  constructor(issues: readonly ConditionIssue[], conditions: unknown) {
    super(`Unsupported policy condition: ${formatConditionIssues(issues)}`);
    this.name = UNSUPPORTED_CONDITION_ERROR_NAME;
    this.issues = issues;
    this.conditions = conditions;
    Object.setPrototypeOf(this, UnsupportedConditionError.prototype);
  }
}

export function isUnsupportedConditionError(value: unknown): value is UnsupportedConditionError {
  if (value instanceof UnsupportedConditionError) {
    return true;
  }
  const candidate = value as { name?: unknown; issues?: unknown } | null;
  return (
    typeof candidate === 'object' &&
    candidate !== null &&
    candidate.name === UNSUPPORTED_CONDITION_ERROR_NAME &&
    Array.isArray(candidate.issues)
  );
}

export class SqlRenderError extends Error {
  readonly path: readonly string[];

  constructor(message: string, path: readonly string[]) {
    super(`${message} (at ${formatConditionPath(path)})`);
    this.name = 'SqlRenderError';
    this.path = path;
    Object.setPrototypeOf(this, SqlRenderError.prototype);
  }
}
