import { DatamodelField, DatamodelModel } from './datamodel';

export interface ModelMetadata {
  readonly model: DatamodelModel;
  readonly fieldsByName: ReadonlyMap<string, DatamodelField>;
  readonly scalarFields: readonly DatamodelField[];
  readonly relations: readonly DatamodelField[];
  readonly primaryKey?: DatamodelField;
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
    const scalarFields = Object.freeze(model.fields.filter((field) => field.kind !== 'object'));
    const relations = Object.freeze(model.fields.filter((field) => field.kind === 'object'));
    return [
      model.name,
      Object.freeze({
        model,
        fieldsByName: new ImmutableMap(model.fields.map((field) => [field.name, field] as const)),
        scalarFields,
        relations,
        primaryKey: scalarFields.find((field) => field.isId),
      }),
    ];
  });
  return new ImmutableMap(entries);
}
