import pluralize from 'pluralize';

export function lcFirst(name: string): string {
  return name.charAt(0).toLowerCase() + name.slice(1);
}

export function ucFirst(name: string): string {
  return name.charAt(0).toUpperCase() + name.slice(1);
}

function pluralOrList(name: string): string {
  const plural = pluralize(name);
  return plural === name ? `${name}List` : plural;
}

export function findOneFieldName(model: string): string {
  return lcFirst(model);
}

export function findManyFieldName(model: string): string {
  return pluralOrList(lcFirst(model));
}

export function createFieldName(model: string): string {
  return `create${model}`;
}

export function updateFieldName(model: string): string {
  return `update${model}`;
}

export function deleteFieldName(model: string): string {
  return `delete${model}`;
}

export function updateManyFieldName(model: string): string {
  return `updateMany${pluralOrList(model)}`;
}

export function deleteManyFieldName(model: string): string {
  return `deleteMany${pluralOrList(model)}`;
}

export function eventsFieldName(model: string): string {
  return `${lcFirst(model)}Events`;
}

export function relationCountTypeName(model: string): string {
  return `${model}CountOutputType`;
}

export function aggregateFieldName(model: string): string {
  return `${pluralOrList(lcFirst(model))}Aggregate`;
}

export function groupByFieldName(model: string): string {
  return `${pluralOrList(lcFirst(model))}Grouped`;
}
