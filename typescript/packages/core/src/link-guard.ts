import { GolemConflictError, GolemNotFoundError } from './errors';
import { LockRequest, RowLocker, RowLocks } from './cascade';
import {
  LinkRemoval,
  LinkTarget,
  collectLinkRemovals,
  collectLinkTargets,
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
  locker(client: Client): RowLocker;
}

export class LinkGuard {
  private readonly preExisting = new Set<LinkTarget>();

  private constructor(
    private readonly port: LinkGuardPort,
    private readonly model: string,
    private readonly targets: readonly LinkTarget[],
    private readonly removals: readonly LinkRemoval[],
  ) {}

  static of(port: LinkGuardPort, model: string, data: unknown, enforced: boolean): LinkGuard {
    if (!enforced) {
      return new LinkGuard(port, model, [], []);
    }
    return new LinkGuard(
      port,
      model,
      collectLinkTargets(port.metadata, model, data),
      collectLinkRemovals(port.metadata, model, data, (target, where) => port.unwrap(target, where)),
    );
  }

  get needsRoot(): boolean {
    return this.needsTransaction;
  }

  get needsTransaction(): boolean {
    return this.targets.length + this.removals.length > 0;
  }

  private key(model: string, row: Row): string {
    return linkedRowKey(this.port.metadata, model, row);
  }

  async before(client: Client, root?: Row): Promise<void> {
    if (!this.needsTransaction) {
      return;
    }
    const requests: LockRequest[] = root ? [{ model: this.model, row: root, mode: 'UPDATE' }] : [];
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
        if (!target.createsWhenMissing) {
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
      const readable = await this.port.readable(removal.model, where, client);
      if (readable.length !== current.length) {
        throw new GolemNotFoundError(`${removal.model} not found`);
      }
    }
    for (const { target, where } of found) {
      if (!(await this.port.readableRow(target.model, where, client))) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      this.preExisting.add(target);
    }
  }

  async after(client: Client): Promise<ReadonlySet<string>> {
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
