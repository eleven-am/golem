export type PathSegment = string | number

export interface GraphQLErrorShape {
  readonly message: string
  readonly path?: ReadonlyArray<PathSegment>
  readonly locations?: ReadonlyArray<{ readonly line: number; readonly column: number }>
  readonly extensions?: Readonly<Record<string, unknown>>
}

export type CommitState = 'PARTIAL' | 'UNKNOWN'

export const CODE_PROTOCOL_MISMATCH = 'GOLEM_PROTOCOL_MISMATCH'
export const CODE_CONTRACT_MISMATCH = 'GOLEM_CONTRACT_MISMATCH'
export const CODE_DEADLINE_REQUIRED = 'GOLEM_PRINCIPAL_DEADLINE_REQUIRED'
export const CODE_UNCODED = 'GOLEM_UNCODED'

export class GolemError extends Error {
  readonly code: string
  readonly path: ReadonlyArray<PathSegment> | undefined
  readonly extensions: Readonly<Record<string, unknown>>
  readonly retryable: boolean

  constructor(shape: GraphQLErrorShape, retryable = true) {
    super(shape.message)
    this.name = new.target.name
    const extensions = shape.extensions ?? {}
    this.code = typeof extensions.code === 'string' ? extensions.code : CODE_UNCODED
    this.path = shape.path
    this.extensions = extensions
    this.retryable = retryable
  }
}

export class GolemProtocolMismatchError extends GolemError {
  constructor(shape: GraphQLErrorShape) {
    super(shape, false)
  }
}

export class GolemContractMismatchError extends GolemError {
  constructor(shape: GraphQLErrorShape) {
    super(shape, false)
  }
}

export class GolemDeadlineRequiredError extends GolemError {
  constructor(shape: GraphQLErrorShape) {
    super(shape, false)
  }
}

export class GolemUnauthenticatedError extends GolemError {
  constructor(message: string) {
    super({ message, extensions: { code: 'GOLEM_UNAUTHENTICATED' } }, false)
  }
}

export class GolemHttpError extends GolemError {
  readonly status: number

  constructor(status: number, message: string) {
    super({ message, extensions: { code: `GOLEM_HTTP_${status}` } })
    this.status = status
  }
}

export abstract class GolemCommitError extends GolemError {
  readonly errors: ReadonlyArray<GolemError>
  readonly data: unknown

  constructor(first: GolemError, errors: ReadonlyArray<GolemError>, data: unknown) {
    super({ message: first.message, path: first.path, extensions: first.extensions }, false)
    this.errors = errors
    this.data = data
  }
}

export class GolemPartialCommitError extends GolemCommitError {}

export class GolemCommitUnknownError extends GolemCommitError {}

export class GolemProtocolError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'GolemProtocolError'
  }
}

export class GolemConfigurationError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'GolemConfigurationError'
  }
}

export function toGolemError(shape: GraphQLErrorShape): GolemError {
  switch (shape.extensions?.code) {
    case CODE_PROTOCOL_MISMATCH:
      return new GolemProtocolMismatchError(shape)
    case CODE_CONTRACT_MISMATCH:
      return new GolemContractMismatchError(shape)
    case CODE_DEADLINE_REQUIRED:
      return new GolemDeadlineRequiredError(shape)
    default:
      return new GolemError(shape)
  }
}

export interface GraphQLResponseBody {
  readonly data?: unknown
  readonly errors?: ReadonlyArray<GraphQLErrorShape>
  readonly extensions?: { readonly golem?: { readonly commit?: unknown; readonly live?: unknown } }
}

export function commitState(body: GraphQLResponseBody): CommitState | undefined {
  const commit = body.extensions?.golem?.commit
  if (commit === undefined) {
    return undefined
  }
  if (commit === 'PARTIAL' || commit === 'UNKNOWN') {
    return commit
  }
  throw new GolemProtocolError(`unrecognised extensions.golem.commit value ${JSON.stringify(commit)}`)
}

export function errorsOf(body: GraphQLResponseBody): GolemError[] {
  return (body.errors ?? []).map(toGolemError)
}

export function failureOf(body: GraphQLResponseBody): GolemError | undefined {
  const errors = errorsOf(body)
  const first = errors[0]
  if (first === undefined) {
    return undefined
  }
  switch (commitState(body)) {
    case 'PARTIAL':
      return new GolemPartialCommitError(first, errors, body.data)
    case 'UNKNOWN':
      return new GolemCommitUnknownError(first, errors, body.data)
    default:
      return first
  }
}
