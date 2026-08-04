import { DatamodelField, DatamodelModel } from './datamodel';

export interface ModelMetadata {
  readonly model: DatamodelModel;
  readonly fieldsByName: ReadonlyMap<string, DatamodelField>;
  readonly scalarFields: readonly DatamodelField[];
  readonly relations: readonly DatamodelField[];
  readonly primaryKey?: DatamodelField;
  readonly primaryKeys: readonly DatamodelField[];
  readonly compoundKeyName?: string;
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

export function buildModelMetadata(models: readonly DatamodelModel[]): ModelMetadataIndex {
  const entries = models.map((model): readonly [string, ModelMetadata] => {
    const fieldsByName = new ImmutableMap(model.fields.map((field) => [field.name, field] as const));
    const scalarFields = Object.freeze(model.fields.filter((field) => field.kind !== 'object'));
    const relations = Object.freeze(model.fields.filter((field) => field.kind === 'object'));
    const singleId = scalarFields.find((field) => field.isId);
    const compound = model.primaryKey;
    let primaryKeys: readonly DatamodelField[];
    let compoundKeyName: string | undefined;
    if (compound && compound.fields.length > 0) {
      primaryKeys = Object.freeze(
        compound.fields
          .map((name) => fieldsByName.get(name))
          .filter((entry): entry is DatamodelField => entry !== undefined),
      );
      compoundKeyName = compound.name ?? compound.fields.join('_');
    } else {
      primaryKeys = Object.freeze(singleId ? [singleId] : []);
    }
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
        primaryKey: singleId,
        primaryKeys,
        compoundKeyName,
        compoundUniqueSelectors,
      }),
    ];
  });
  return new ImmutableMap(entries);
}
