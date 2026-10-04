import { GolemConflictError, GolemNotFoundError } from './errors';
import { LockRequest, RowLocker, RowLocks } from './cascade';
import {
  BranchCondition,
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
  private active: readonly LinkTarget[] = [];

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
      collectLinkTargets(port.metadata, model, data),
      collectLinkRemovals(port.metadata, model, data, unwrap),
      collectNestedMutations(port.metadata, model, data, unwrap),
    );
  }

  get needsRoot(): boolean {
    return this.needsTransaction;
  }

  get needsTransaction(): boolean {
    return (this.enforced && this.targets.length + this.removals.length > 0) || this.port.locksRows;
  }

  private key(model: string, row: Row): string {
    return linkedRowKey(this.port.metadata, model, row);
  }

  async before(client: Client, roots: readonly Row[]): Promise<void> {
    if (!this.needsTransaction) {
      return;
    }
    const root = roots[0];
    const requests: LockRequest[] = roots.map((row) => ({ model: this.model, row, mode: 'UPDATE' as const }));
    const mutated: Array<{ model: string; where: unknown; rows: Row[] }> = [];
    for (const mutation of this.mutations) {
      const where = mutation.linkedTo(root!);
      const rows = await this.port.findMany(mutation.model, where, client);
      mutated.push({ model: mutation.model, where, rows });
      requests.push(...rows.map((row) => ({ model: mutation.model, row, mode: mutation.lock })));
    }
    const removed: Array<{ removal: LinkRemoval; where: unknown; rows: Row[] }> = [];
    for (const removal of this.removals) {
      const where = removal.linkedTo(root!);
      const rows = await this.port.findMany(removal.model, where, client);
      removed.push({ removal, where, rows });
      requests.push(...rows.map((row) => ({ model: removal.model, row, mode: removal.lock })));
    }
    const conditions = [...new Set(this.targets.flatMap((target) => target.when))];
    const branchFilter = (condition: BranchCondition): unknown => {
      const where = this.port.unwrap(condition.model, condition.where);
      return condition.parent ? { AND: [where, condition.parent(root!)] } : where;
    };
    const presence = async (): Promise<Map<BranchCondition, Row | null>> => {
      const rows = new Map<BranchCondition, Row | null>();
      for (const condition of conditions) {
        rows.set(condition, await this.port.findFirst(condition.model, branchFilter(condition), client));
      }
      return rows;
    };
    const decided = await presence();
    for (const [condition, row] of decided) {
      if (row) {
        requests.push({ model: condition.model, row, mode: 'SHARE' });
      }
    }
    this.active = this.targets.filter((target) =>
      target.when.every((condition) => (decided.get(condition) !== null) === condition.present));
    const found: Array<{ target: LinkTarget; where: unknown }> = [];
    for (const target of this.active) {
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
    const rechecked = await presence();
    for (const condition of conditions) {
      if ((decided.get(condition) === null) !== (rechecked.get(condition) === null)) {
        throw new GolemConflictError(`The ${condition.model} row deciding this nested write changed concurrently`);
      }
    }
    for (const { model, where, rows } of mutated) {
      await this.refuseChanged(model, where, rows, client);
    }
    for (const { removal, where, rows } of removed) {
      const current = await this.refuseChanged(removal.model, where, rows, client);
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

  private async refuseChanged(model: string, where: unknown, locked: readonly Row[], client: Client): Promise<Row[]> {
    const current = await this.port.findMany(model, where, client);
    const before = locked.map((row) => this.key(model, row)).sort();
    const after = current.map((row) => this.key(model, row)).sort();
    if (before.length !== after.length || before.some((key, index) => key !== after[index])) {
      throw new GolemConflictError(`The ${model} rows linked to this ${this.model} changed concurrently`);
    }
    return current;
  }

  async after(client: Client): Promise<ReadonlySet<string>> {
    if (!this.enforced) {
      return new Set();
    }
    const stored: Array<{ target: LinkTarget; where: unknown }> = [];
    const requests: LockRequest[] = [];
    for (const target of this.active) {
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
