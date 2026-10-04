import { DatamodelField, DatamodelModel, rowIdentity } from './datamodel';

export interface ModelMetadata {
  readonly model: DatamodelModel;
  readonly fieldsByName: ReadonlyMap<string, DatamodelField>;
  readonly scalarFields: readonly DatamodelField[];
  readonly relations: readonly DatamodelField[];
  readonly identityFields: readonly DatamodelField[];
  readonly identitySelector?: string;
  readonly compoundKeyName?: string;
  readonly compoundKeyFields: readonly string[];
  readonly compoundUniqueSelectors: ReadonlyMap<string, readonly string[]>;
}

export type ModelMetadataIndex = ReadonlyMap<string, ModelMetadata>;

class ImmutableMap<K, V> implements ReadonlyMap<K, V> {
  readonly #data: Map<K, V>;

  constructor(entries: readonly (readonly [K, V])[]) {
    this.#data = new Map(entries);
    Object.freeze(this);
  }

  get size(): number { return this.#data.size; }
  get(key: K): V | undefined { return this.#data.get(key); }
  has(key: K): boolean { return this.#data.has(key); }
  entries(): MapIterator<[K, V]> { return this.#data.entries(); }
  keys(): MapIterator<K> { return this.#data.keys(); }
  values(): MapIterator<V> { return this.#data.values(); }
  [Symbol.iterator](): MapIterator<[K, V]> { return this.#data[Symbol.iterator](); }
  forEach(callbackfn: (value: V, key: K, map: ReadonlyMap<K, V>) => void, thisArg?: unknown): void {
    this.#data.forEach((value, key) => callbackfn.call(thisArg, value, key, this));
  }
}

export interface FlattenedSelectors {
  readonly where: unknown;
  readonly members: ReadonlySet<string>;
}

export function flattenUniqueSelectors(meta: ModelMetadata | undefined, where: unknown): FlattenedSelectors {
  const members = new Set<string>();
  if (!meta || !where || typeof where !== 'object' || Array.isArray(where)) {
    return { where, members };
  }
  const selectors = new Set<string>(meta.compoundUniqueSelectors.keys());
  if (meta.compoundKeyName) {
    selectors.add(meta.compoundKeyName);
  }
  if (selectors.size === 0) {
    return { where, members };
  }
  const rest: Record<string, unknown> = {};
  const flattened: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(where as Record<string, unknown>)) {
    if (
      selectors.has(key) && !meta.fieldsByName.has(key) &&
      value && typeof value === 'object' && !Array.isArray(value)
    ) {
      Object.assign(flattened, value as Record<string, unknown>);
      for (const member of Object.keys(value)) {
        members.add(member);
      }
    } else {
      rest[key] = value;
    }
  }
  return { where: members.size > 0 ? { ...rest, ...flattened } : where, members };
}

export function buildModelMetadata(models: readonly DatamodelModel[]): ModelMetadataIndex {
  const entries = models.map((model): readonly [string, ModelMetadata] => {
    const fieldsByName = new ImmutableMap(model.fields.map((field) => [field.name, field] as const));
    const scalarFields = Object.freeze(model.fields.filter((field) => field.kind !== 'object'));
    const relations = Object.freeze(model.fields.filter((field) => field.kind === 'object'));
    const compound = model.primaryKey?.fields.length ? model.primaryKey : undefined;
    const compoundKeyName = compound ? compound.name ?? compound.fields.join('_') : undefined;
    const compoundKeyFields = Object.freeze([...(compound?.fields ?? [])]);
    const identity = rowIdentity(model);
    const identityFields = Object.freeze((identity?.fields ?? []).map((name) => fieldsByName.get(name)!));
    const identitySelector = identity?.selector;
    const compoundUniqueSelectors = new ImmutableMap(
      (model.uniqueIndexes ?? [])
        .filter((index) => index.fields.length > 1)
        .map((index): readonly [string, readonly string[]] => [
          index.name ?? index.fields.join('_'),
          Object.freeze([...index.fields]),
        ]),
    );
    return [
      model.name,
      Object.freeze({
        model,
        fieldsByName,
        scalarFields,
        relations,
        identityFields,
        identitySelector,
        compoundKeyName,
        compoundKeyFields,
        compoundUniqueSelectors,
      }),
    ];
  });
  return new ImmutableMap(entries);
}
