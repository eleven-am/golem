import { GolemNotFoundError } from './errors';
import { LinkRemoval, LinkTarget, collectLinkRemovals, collectLinkTargets, linkedRowKey } from './link-targets';
import { ModelMetadataIndex } from './model-meta';

type Row = Record<string, unknown>;
type Client = Record<string, any>;

export type LockMode = 'UPDATE' | 'SHARE';

export interface LinkGuardPort {
  readonly metadata: ModelMetadataIndex;
  unwrap(model: string, where: unknown): unknown;
  findFirst(model: string, where: unknown, client: Client): Promise<Row | null>;
  findMany(model: string, where: unknown, client: Client): Promise<Row[]>;
  readable(model: string, where: unknown, client: Client): Promise<Row[]>;
  readableRow(model: string, where: unknown, client: Client): Promise<Row | null>;
  lock(model: string, rows: readonly Row[], mode: LockMode, client: Client): Promise<void>;
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
    return this.removals.length > 0;
  }

  get needsTransaction(): boolean {
    return this.targets.length + this.removals.length > 0;
  }

  async before(client: Client, root?: Row): Promise<void> {
    if (this.removals.length > 0) {
      await this.port.lock(this.model, [root!], 'UPDATE', client);
    }
    for (const removal of this.removals) {
      const where = removal.linkedTo(root!);
      const linked = await this.port.findMany(removal.model, where, client);
      await this.port.lock(removal.model, linked, 'SHARE', client);
      const readable = await this.port.readable(removal.model, where, client);
      if (readable.length !== linked.length) {
        throw new GolemNotFoundError(`${removal.model} not found`);
      }
    }
    for (const target of this.targets) {
      const where = this.port.unwrap(target.model, target.where);
      const row = await this.port.findFirst(target.model, where, client);
      if (!row) {
        if (!target.createsWhenMissing) {
          throw new GolemNotFoundError(`${target.model} not found`);
        }
        continue;
      }
      await this.port.lock(target.model, [row], 'SHARE', client);
      if (!(await this.port.readableRow(target.model, where, client))) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      this.preExisting.add(target);
    }
  }

  async after(client: Client): Promise<ReadonlySet<string>> {
    const linked = new Set<string>();
    for (const target of this.targets) {
      const where = this.port.unwrap(target.model, target.where);
      const stored = await this.port.findFirst(target.model, where, client);
      if (!stored) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      await this.port.lock(target.model, [stored], 'SHARE', client);
      const row = await this.port.readableRow(target.model, where, client);
      if (!row) {
        throw new GolemNotFoundError(`${target.model} not found`);
      }
      if (this.preExisting.has(target)) {
        linked.add(linkedRowKey(this.port.metadata, target.model, row));
      }
    }
    return linked;
  }
}
