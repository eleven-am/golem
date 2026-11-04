import { type GolemError, GolemContractMismatchError, GolemDeadlineRequiredError, GolemProtocolError, GolemProtocolMismatchError, GolemUnauthenticatedError, GolemError as GolemErrorClass, CODE_CONTRACT_MISMATCH, CODE_DEADLINE_REQUIRED, CODE_PROTOCOL_MISMATCH, errorsOf, type GraphQLResponseBody, type GraphQLErrorShape } from './errors.js'

export const PROTOCOL_VERSION = 1

export interface GolemBlock {
  readonly protocol: number
  readonly contract: string
}

export interface ContractManifest {
  readonly contract: string
}

export function golemBlock(manifest: ContractManifest): GolemBlock {
  return { protocol: PROTOCOL_VERSION, contract: manifest.contract }
}

export const CloseCode = {
  BadRequest: 4400,
  Unauthorized: 4401,
  Forbidden: 4403,
  InitTimeout: 4408,
  SubscriberExists: 4409,
  HandshakeRefused: 4426,
  TooManyInits: 4429,
} as const

export type SessionStatus =
  | 'idle'
  | 'connecting'
  | 'open'
  | 'reconnecting'
  | 'unauthenticated'
  | 'forbidden'
  | 'invalid'
  | 'protocol-mismatch'
  | 'contract-mismatch'
  | 'deadline-required'

export type CloseDisposition =
  | { readonly action: 'retry' }
  | { readonly action: 'reauthenticate' }
  | { readonly action: 'fatal'; readonly status: SessionStatus; readonly error: GolemError | GolemProtocolError }

const REFUSAL_PREFIX = 'golem:'

function refusalShape(message: string, code: string): GraphQLErrorShape {
  return { message, extensions: { code } }
}

export function handshakeRefusal(reason: string): CloseDisposition & { readonly action: 'fatal' } {
  const detail = reason.startsWith(REFUSAL_PREFIX) ? reason.slice(REFUSAL_PREFIX.length) : reason
  switch (detail) {
    case 'protocol':
      return {
        action: 'fatal',
        status: 'protocol-mismatch',
        error: new GolemProtocolMismatchError(refusalShape('the server refused the live protocol version this client speaks', CODE_PROTOCOL_MISMATCH)),
      }
    case 'contract':
      return {
        action: 'fatal',
        status: 'contract-mismatch',
        error: new GolemContractMismatchError(refusalShape('the client was generated from a different contract than this server; regenerate the client', CODE_CONTRACT_MISMATCH)),
      }
    case CODE_DEADLINE_REQUIRED:
      return {
        action: 'fatal',
        status: 'deadline-required',
        error: new GolemDeadlineRequiredError(refusalShape('the server refused the socket because the principal has no credential expiry', CODE_DEADLINE_REQUIRED)),
      }
    default:
      return {
        action: 'fatal',
        status: 'protocol-mismatch',
        error: new GolemProtocolError(`the server refused the handshake with an unrecognised reason ${JSON.stringify(reason)}`),
      }
  }
}

export function classifyClose(code: number, reason: string, acknowledged: boolean): CloseDisposition {
  switch (code) {
    case CloseCode.HandshakeRefused:
      return handshakeRefusal(reason)
    case CloseCode.Unauthorized:
      if (acknowledged) {
        return { action: 'reauthenticate' }
      }
      return {
        action: 'fatal',
        status: 'unauthenticated',
        error: new GolemUnauthenticatedError('the server closed the socket with 4401 before the connection was acknowledged: the credentials were rejected'),
      }
    case CloseCode.Forbidden:
      return { action: 'fatal', status: 'forbidden', error: new GolemErrorClass({ message: 'the server refused connection initialisation (4403)', extensions: { code: 'GOLEM_FORBIDDEN' } }, false) }
    case CloseCode.BadRequest:
    case CloseCode.SubscriberExists:
    case CloseCode.TooManyInits:
      return { action: 'fatal', status: 'invalid', error: new GolemProtocolError(`the server closed the socket with ${code}: the client broke the protocol`) }
    default:
      return { action: 'retry' }
  }
}

export const BACKOFF_BASE_MS = 250
export const BACKOFF_CAP_MS = 30_000

export function backoffCeiling(attempt: number): number {
  return Math.min(BACKOFF_CAP_MS, BACKOFF_BASE_MS * 2 ** Math.max(0, attempt))
}

export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  return Math.floor(random() * backoffCeiling(attempt))
}

export function isFatalHandshakeStatus(status: SessionStatus): boolean {
  return status === 'protocol-mismatch' || status === 'contract-mismatch'
}

export type AckOutcome =
  | { readonly kind: 'ack' }
  | { readonly kind: 'event'; readonly body: GraphQLResponseBody }
  | { readonly kind: 'error'; readonly errors: GolemError[] }
  | { readonly kind: 'violation'; readonly error: GolemProtocolError }

export class AckTracker {
  private acknowledged = false

  get live(): boolean {
    return this.acknowledged
  }

  reset(): void {
    this.acknowledged = false
  }

  accept(body: GraphQLResponseBody): AckOutcome {
    if (body.extensions?.golem?.live === true) {
      if (this.acknowledged) {
        return { kind: 'violation', error: new GolemProtocolError('a second live acknowledgement arrived for one operation') }
      }
      if (body.data !== null || (body.errors?.length ?? 0) !== 0) {
        return { kind: 'violation', error: new GolemProtocolError('the live acknowledgement carried data or errors') }
      }
      this.acknowledged = true
      return { kind: 'ack' }
    }
    if ((body.errors?.length ?? 0) !== 0) {
      return { kind: 'error', errors: errorsOf(body) }
    }
    if (!this.acknowledged) {
      return { kind: 'violation', error: new GolemProtocolError('an event arrived before the live acknowledgement') }
    }
    return { kind: 'event', body }
  }
}
