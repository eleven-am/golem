import { GolemConflictError, GolemNotFoundError } from './errors';
import { LockRequest, RowLocker, RowLocks } from './cascade';
import {
  LinkRemoval,
  LinkTarget,
  collectLinkRemovals,
  collectLinkTargets,
  collectNestedMutations,
  linkedRowKey,
} from './link-targets';
import { ModelMetadataIndex } from './model-meta';

type Row = Record<string, unknown>;
type Client = Record<string, any>;

export interface LinkGuardPort {
  readonly metadata: ModelMetadataIndex;
  unwrap(model: string, where: unknown): unknown;
  findFirst(model: string, where: unknown, client: Client): Promise<Row | null>;
  findMany(model: string, where: unknown, client: Client): Promise<Row[]>;
  readable(model: string, where: unknown, client: Client): Promise<Row[]>;
  readableRow(model: string, where: unknown, client: Client): Promise<Row | null>;
  locks(client: Client): RowLocks;
  readonly locksRows: boolean;
  locker(client: Client): RowLocker;
}

export class LinkGuard {
  private readonly preExisting = new Set<LinkTarget>();

  private constructor(
    private readonly port: LinkGuardPort,
    private readonly model: string,
    private readonly enforced: boolean,
    private readonly targets: readonly LinkTarget[],
    private readonly removals: readonly LinkRemoval[],
    private readonly mutations: readonly LinkRemoval[],
  ) {}

  static of(port: LinkGuardPort, model: string, data: unknown, enforced: boolean): LinkGuard {
    const unwrap = (target: string, where: unknown) => port.unwrap(target, where);
    return new LinkGuard(
      port,
      model,
      enforced,
      collectLinkTargets(port.metadata, model, data, enforced),
      collectLinkRemovals(port.metadata, model, data, unwrap),
      collectNestedMutations(port.metadata, model, data, unwrap),
    );
  }

  get needsRoot(): boolean {
    return this.needsTransaction;
  }

  get needsTransaction(): boolean {
    const links = this.targets.length + this.removals.length;
    return (this.enforced && links > 0) || (this.port.locksRows && links + this.mutations.length > 0);
  }

  private key(model: string, row: Row): string {
    return linkedRowKey(this.port.metadata, model, row);
  }

  async before(client: Client, roots: readonly Row[]): Promise<void> {
    if (!this.needsTransaction && !(this.port.locksRows && roots.length > 1)) {
      return;
    }
    const root = roots[0];
    const requests: LockRequest[] = roots.map((row) => ({ model: this.model, row, mode: 'UPDATE' as const }));
    for (const mutation of this.mutations) {
      const rows = await this.port.findMany(mutation.model, mutation.linkedTo(root!), client);
      requests.push(...rows.map((row) => ({ model: mutation.model, row, mode: mutation.lock })));
    }
    const removed: Array<{ removal: LinkRemoval; where: unknown; rows: Row[] }> = [];
    for (const removal of this.removals) {
      const where = removal.linkedTo(root!);
      const rows = await this.port.findMany(removal.model, where, client);
      removed.push({ removal, where, rows });
      requests.push(...rows.map((row) => ({ model: removal.model, row, mode: removal.lock })));
    }
    const found: Array<{ target: LinkTarget; where: unknown }> = [];
    for (const target of this.targets) {
      const where = this.port.unwrap(target.model, target.where);
      const row = await this.port.findFirst(target.model, where, client);
      if (!row) {
        if (this.enforced && !target.createsWhenMissing) {
          throw new GolemNotFoundError(`${target.model} not found`);
        }
        continue;
      }
      found.push({ target, where });
      requests.push({ model: target.model, row, mode: target.lock });
    }
    await this.port.locks(client).acquire(requests, this.port.locker(client));
    for (const { removal, where, rows } of removed) {
      const current = await this.port.findMany(removal.model, where, client);
      const locked = new Set(rows.map((row) => this.key(removal.model, row)));
      if (current.some((row) => !locked.has(this.key(removal.model, row)))) {
        throw new GolemConflictError(`The ${removal.model} rows linked to this ${this.model} changed concurrently`);
      }
      if (!this.enforced) {
        continue;
      }
      const readable = await this.port.readable(removal.model, where, client);
      if (readable.length !== current.length) {
        throw new GolemNotFoundError(`${removal.model} not found`);
      }
    }
    for (const { target, where } of found) {
      if (!this.enforced) {
        continue;
      }
      if (!(await this.port.readableRow(target.model, where, client))) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      this.preExisting.add(target);
    }
  }

  async after(client: Client): Promise<ReadonlySet<string>> {
    if (!this.enforced) {
      return new Set();
    }
    const stored: Array<{ target: LinkTarget; where: unknown }> = [];
    const requests: LockRequest[] = [];
    for (const target of this.targets) {
      const where = this.port.unwrap(target.model, target.where);
      const row = await this.port.findFirst(target.model, where, client);
      if (!row) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      stored.push({ target, where });
      requests.push({ model: target.model, row, mode: 'SHARE' });
    }
    await this.port.locks(client).acquire(requests, this.port.locker(client));
    const linked = new Set<string>();
    for (const { target, where } of stored) {
      const row = await this.port.readableRow(target.model, where, client);
      if (!row) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      if (this.preExisting.has(target)) {
        linked.add(this.key(target.model, row));
      }
    }
    return linked;
  }
}
