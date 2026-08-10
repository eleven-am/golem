export const D0 = new Date('2020-01-01T00:00:00.000Z');
export const D1 = new Date('2021-06-15T12:30:00.000Z');
export const D2 = new Date('2022-12-31T23:59:59.000Z');

export const SAFE = 9007199254740992n;
export const BEYOND = 9007199254740993n;
export const BELOW = -9007199254740993n;

export interface RelatedSeed {
  id: number;
  tag: string;
  tagNull: string | null;
  num: number;
  numNull: number | null;
  big: bigint;
  at: Date;
  nextId: number | null;
}

export interface ParentSeed {
  id: number;
  name: string;
  nameNull: string | null;
  count: number;
  countNull: number | null;
  big: bigint;
  bigNull: bigint | null;
  at: Date;
  atNull: Date | null;
  relatedId: number | null;
}

export const RELATED: readonly RelatedSeed[] = [
  { id: 1, tag: 'red', tagNull: 'crimson', num: 1, numNull: 10, big: 1n, at: D0, nextId: null },
  { id: 2, tag: 'blue', tagNull: null, num: 0, numNull: null, big: BEYOND, at: D1, nextId: 1 },
  { id: 3, tag: '', tagNull: '', num: -5, numNull: 0, big: 0n, at: D2, nextId: 2 },
];

export const PARENTS: readonly ParentSeed[] = [
  {
    id: 1, name: 'alpha', nameNull: 'alpha', count: 0, countNull: 0,
    big: 0n, bigNull: 0n, at: D0, atNull: D0, relatedId: 1,
  },
  {
    id: 2, name: '', nameNull: null, count: -1, countNull: null,
    big: 1n, bigNull: null, at: D1, atNull: null, relatedId: 2,
  },
  {
    id: 3, name: 'beta', nameNull: '', count: 7, countNull: 7,
    big: SAFE, bigNull: SAFE, at: D2, atNull: D2, relatedId: 3,
  },
  {
    id: 4, name: 'Zulu', nameNull: 'zulu', count: 100, countNull: -1,
    big: BEYOND, bigNull: BEYOND, at: D0, atNull: D1, relatedId: null,
  },
  {
    id: 5, name: '0', nameNull: null, count: 0, countNull: null,
    big: BELOW, bigNull: null, at: D1, atNull: null, relatedId: null,
  },
  {
    id: 6, name: 'alpha', nameNull: 'omega', count: 7, countNull: 0,
    big: 1n, bigNull: 2n, at: D2, atNull: D0, relatedId: 1,
  },
  {
    id: 7, name: 'gamma', nameNull: null, count: -1, countNull: 100,
    big: BEYOND, bigNull: -1n, at: D0, atNull: null, relatedId: 2,
  },
  {
    id: 8, name: 'delta', nameNull: 'delta', count: 100, countNull: 7,
    big: 2n, bigNull: SAFE, at: D1, atNull: D2, relatedId: null,
  },
];
