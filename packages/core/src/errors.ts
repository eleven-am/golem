export type GolemErrorCode =
  | 'NOT_FOUND'
  | 'CONFLICT'
  | 'BAD_USER_INPUT'
  | 'UNAUTHENTICATED'
  | 'FORBIDDEN';

export class GolemError extends Error {
  constructor(
    message: string,
    readonly code: GolemErrorCode,
  ) {
    super(message);
    this.name = new.target.name;
  }
}

export class GolemNotFoundError extends GolemError {
  constructor(message: string) {
    super(message, 'NOT_FOUND');
  }
}

export class GolemConflictError extends GolemError {
  constructor(message: string) {
    super(message, 'CONFLICT');
  }
}

export class GolemValidationError extends GolemError {
  constructor(message: string) {
    super(message, 'BAD_USER_INPUT');
  }
}

export class GolemUnauthorizedError extends GolemError {
  constructor(message: string) {
    super(message, 'UNAUTHENTICATED');
  }
}

export class GolemForbiddenError extends GolemError {
  constructor(message: string) {
    super(message, 'FORBIDDEN');
  }
}
