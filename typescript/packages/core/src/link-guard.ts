import { GolemConflictError, GolemNotFoundError } from './errors';
import { LockRequest, RowLocker, RowLocks } from './cascade';
import type { GolemProvider } from './datamodel';
import {
  LinkRemoval,
  LinkTarget,
  collectLinkRemovals,
  collectLinkTargets,
  collectNestedMutations,
  hasNestedBranches,
  linkedRowKey,
  linkTargetKey,
  resolveNestedBranches,
} from './link-targets';
import { canonicalToken } from './canonical';
import { ModelMetadataIndex } from './model-meta';
import { GOLEM_UPSERT_GUARD_MODEL, UpsertGuardDelegate, acquireUpsertGuard } from './upsert-guard';

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
  upsertGuard(client: Client): UpsertGuardDelegate;
  readonly provider: GolemProvider | undefined;
  readonly upsertGuardStripes: number;
}

export async function decideBranch(
  port: LinkGuardPort,
  client: Client,
  model: string,
  selector: unknown,
  find: () => Promise<Row | null>,
): Promise<Row | null> {
  const locks = port.locks(client);
  const locker = port.locker(client);
  await acquireUpsertGuard(port.upsertGuard(client), model, selector, port.upsertGuardStripes, port.provider, async (row) => {
    let present = true;
    await locks.acquire([{ model: GOLEM_UPSERT_GUARD_MODEL, row, mode: 'UPDATE' }], async (requests, wait) => {
      present = await locker(requests, wait);
      return present;
    });
    return present;
  });
  const decided = await find();
  if (decided) {
    await locks.acquire([{ model, row: decided, mode: 'UPDATE' }], locker);
  }
  const rechecked = await find();
  const key = (row: Row | null) => (row ? linkedRowKey(port.metadata, model, row) : null);
  if (key(decided) !== key(rechecked)) {
    throw new GolemConflictError(`The ${model} row deciding this write changed concurrently`);
  }
  return decided;
}

export class LinkGuard {
  private targets: readonly LinkTarget[] = [];
  private created: LinkTarget[] = [];

  private constructor(
    private readonly port: LinkGuardPort,
    private readonly model: string,
    private readonly enforced: boolean,
    private readonly data: unknown,
    private readonly written: unknown,
    private readonly branching: boolean,
    private readonly linking: boolean,
    private readonly rooted: boolean,
  ) {}

  static of(
    port: LinkGuardPort,
    model: string,
    data: unknown,
    enforced: boolean,
    written: unknown,
    rooted: boolean,
  ): LinkGuard {
    const unwrap = (target: string, where: unknown) => port.unwrap(target, where);
    const branching = hasNestedBranches(port.metadata, model, data) || hasNestedBranches(port.metadata, model, written);
    const linking = [data, written].some((payload) => collectLinkTargets(port.metadata, model, payload).length
      + collectLinkRemovals(port.metadata, model, payload, unwrap).length > 0);
    return new LinkGuard(port, model, enforced, data, written, branching, linking, rooted);
  }

  get needsRoot(): boolean {
    return this.needsTransaction;
  }

  get needsTransaction(): boolean {
    const locking = this.port.provider === 'postgresql' && (this.linking || this.rooted);
    return (this.enforced && this.linking) || this.branching || locking;
  }

  private key(model: string, row: Row): string {
    return linkedRowKey(this.port.metadata, model, row);
  }

  async before(client: Client, roots: readonly Row[]): Promise<unknown> {
    if (!this.needsTransaction) {
      return this.written;
    }
    const root = roots[0];
    const unwrap = (target: string, where: unknown) => this.port.unwrap(target, where);
    const decisions = new Map<string, Promise<Row | null>>();
    const decide = (model: string, filter: unknown): Promise<Row | null> => {
      const key = `${model}\u0000${canonicalToken(filter)}`;
      const known = decisions.get(key);
      if (known) {
        return known;
      }
      const decided = decideBranch(this.port, client, model, filter, () => this.port.findFirst(model, filter, client));
      decisions.set(key, decided);
      return decided;
    };
    this.created = [];
    const data = await resolveNestedBranches(this.port.metadata, this.model, this.data, root, unwrap, decide, this.created);
    const written = this.written === this.data
      ? data
      : await resolveNestedBranches(this.port.metadata, this.model, this.written, root, unwrap, decide);
    this.targets = collectLinkTargets(this.port.metadata, this.model, data);
    const linked = written === data ? this.targets : collectLinkTargets(this.port.metadata, this.model, written);
    const removals = collectLinkRemovals(this.port.metadata, this.model, written, unwrap);
    const named = written === data ? removals : collectLinkRemovals(this.port.metadata, this.model, data, unwrap);
    const requests: LockRequest[] = roots.map((row) => ({ model: this.model, row, mode: 'UPDATE' as const }));
    const mutated: Array<{ model: string; where: unknown; rows: Row[] }> = [];
    for (const mutation of collectNestedMutations(this.port.metadata, this.model, written, unwrap)) {
      const where = mutation.linkedTo(root!);
      const rows = await this.port.findMany(mutation.model, where, client);
      mutated.push({ model: mutation.model, where, rows });
      requests.push(...rows.map((row) => ({ model: mutation.model, row, mode: mutation.lock })));
    }
    const removed: Array<{ removal: LinkRemoval; where: unknown; rows: Row[] }> = [];
    for (const removal of removals) {
      const where = removal.linkedTo(root!);
      const rows = await this.port.findMany(removal.model, where, client);
      removed.push({ removal, where, rows });
      requests.push(...rows.map((row) => ({ model: removal.model, row, mode: removal.lock })));
    }
    const identities = new Map<string, Row | null>();
    for (const target of this.targets) {
      const where = this.port.unwrap(target.model, target.where);
      const row = this.enforced
        ? await this.port.readableRow(target.model, where, client)
        : await this.port.findFirst(target.model, where, client);
      if (!row && this.enforced) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      identities.set(linkTargetKey(target), row);
    }
    for (const target of linked) {
      const key = linkTargetKey(target);
      const row = identities.has(key)
        ? identities.get(key)
        : await this.port.findFirst(target.model, this.port.unwrap(target.model, target.where), client);
      if (row) {
        requests.push({ model: target.model, row, mode: target.lock });
      }
    }
    await this.port.locks(client).acquire(requests, this.port.locker(client));
    for (const { model, where, rows } of mutated) {
      await this.refuseChanged(model, where, rows, client);
    }
    const current = new Map<LinkRemoval, Row[]>();
    for (const { removal, where, rows } of removed) {
      current.set(removal, await this.refuseChanged(removal.model, where, rows, client));
    }
    if (this.enforced) {
      for (const removal of named) {
        const where = removal.linkedTo(root!);
        const rows = current.get(removal) ?? await this.port.findMany(removal.model, where, client);
        const readable = await this.port.readable(removal.model, where, client);
        if (readable.length !== rows.length) {
          throw new GolemNotFoundError(`${removal.model} not found`);
        }
      }
    }
    return written;
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
    const linked = new Set<string>();
    for (const target of [...this.targets, ...this.created]) {
      const row = await this.port.readableRow(target.model, this.port.unwrap(target.model, target.where), client);
      if (!row) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      if (this.targets.includes(target)) {
        linked.add(this.key(target.model, row));
      }
    }
    return linked;
  }
}
