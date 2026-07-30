import { PolicyDatamodel, SqlDialect, SqlParameter } from '../../src/index';
import { WhereCase } from './matrix';
import { Row } from './measure';

function scalar(name: string, dbName: string, type: string) {
  return { name, dbName, kind: 'scalar' as const, type, isList: false };
}

function toOne(name: string, type: string, relationName: string, from: string, to: string) {
  return {
    name,
    kind: 'object' as const,
    type,
    isList: false,
    relationName,
    relationFromFields: [from],
    relationToFields: [to],
  };
}

function toMany(name: string, type: string, relationName: string) {
  return { name, kind: 'object' as const, type, isList: true, relationName };
}

export const LIBRARY_DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'Media',
      dbName: 'media_rows',
      fields: [
        scalar('id', 'media_pk', 'Int'),
        scalar('name', 'media_name', 'String'),
        toMany('groups', 'MediaGroup', 'MediaToGroup'),
        toMany('videos', 'Video', 'MediaToVideo'),
      ],
    },
    {
      name: 'MediaGroup',
      dbName: 'group_rows',
      fields: [
        scalar('id', 'group_pk', 'Int'),
        scalar('label', 'group_label', 'String'),
        scalar('mediaId', 'media_ref', 'Int'),
        toOne('media', 'Media', 'MediaToGroup', 'mediaId', 'id'),
        toMany('policy', 'Policy', 'GroupToPolicy'),
      ],
    },
    {
      name: 'Policy',
      dbName: 'policy_rows',
      fields: [
        scalar('id', 'policy_pk', 'Int'),
        scalar('access', 'access_value', 'String'),
        scalar('groupId', 'group_ref', 'Int'),
        scalar('userGroupSlug', 'usergroup_ref', 'String'),
        toOne('group', 'MediaGroup', 'GroupToPolicy', 'groupId', 'id'),
        toOne('userGroup', 'UserGroup', 'PolicyToUserGroup', 'userGroupSlug', 'slug'),
      ],
    },
    {
      name: 'UserGroup',
      dbName: 'usergroup_rows',
      fields: [
        scalar('slug', 'usergroup_slug', 'String'),
        scalar('name', 'usergroup_name', 'String'),
        toMany('users', 'Membership', 'UserGroupToMembership'),
        toMany('policies', 'Policy', 'PolicyToUserGroup'),
      ],
    },
    {
      name: 'Membership',
      dbName: 'membership_rows',
      fields: [
        scalar('id', 'membership_pk', 'Int'),
        scalar('userId', 'user_ref', 'String'),
        scalar('userGroupSlug', 'usergroup_ref', 'String'),
        toOne('userGroup', 'UserGroup', 'UserGroupToMembership', 'userGroupSlug', 'slug'),
      ],
    },
    {
      name: 'Video',
      dbName: 'video_rows',
      fields: [
        scalar('id', 'video_pk', 'Int'),
        scalar('mediaId', 'media_ref', 'Int'),
        scalar('cloudStorageId', 'storage_ref', 'Int'),
        toOne('media', 'Media', 'MediaToVideo', 'mediaId', 'id'),
        toOne('cloudStorage', 'CloudStorage', 'VideoToStorage', 'cloudStorageId', 'id'),
      ],
    },
    {
      name: 'CloudStorage',
      dbName: 'storage_rows',
      fields: [
        scalar('id', 'storage_pk', 'Int'),
        scalar('userId', 'user_ref', 'String'),
        toMany('videos', 'Video', 'VideoToStorage'),
      ],
    },
  ],
};

export interface UserGroupSeed {
  readonly slug: string;
  readonly name: string;
}

export interface MembershipSeed {
  readonly id: number;
  readonly userId: string;
  readonly userGroupSlug: string;
}

export interface PolicySeed {
  readonly id: number;
  readonly access: string | null;
  readonly groupId: number;
  readonly userGroupSlug: string | null;
}

export interface GroupSeed {
  readonly id: number;
  readonly label: string;
  readonly mediaId: number;
}

export interface MediaSeed {
  readonly id: number;
  readonly name: string;
}

export interface StorageSeed {
  readonly id: number;
  readonly userId: string;
}

export interface VideoSeed {
  readonly id: number;
  readonly mediaId: number;
  readonly cloudStorageId: number | null;
}

export const USER_GROUPS: readonly UserGroupSeed[] = [
  { slug: 'core', name: 'Core' },
  { slug: 'Core', name: 'Core upper' },
  { slug: 'côre', name: 'Core accented' },
  { slug: 'void', name: 'Empty' },
];

export const MEMBERSHIPS: readonly MembershipSeed[] = [
  { id: 1, userId: 'u1', userGroupSlug: 'core' },
  { id: 2, userId: 'u2', userGroupSlug: 'core' },
  { id: 3, userId: 'u2', userGroupSlug: 'Core' },
  { id: 4, userId: 'u1', userGroupSlug: 'côre' },
];

export const POLICIES: readonly PolicySeed[] = [
  { id: 1, access: 'ALLOW', groupId: 1, userGroupSlug: 'core' },
  { id: 2, access: 'DENY', groupId: 1, userGroupSlug: 'Core' },
  { id: 3, access: null, groupId: 2, userGroupSlug: 'core' },
  { id: 4, access: 'ALLOW', groupId: 2, userGroupSlug: null },
  { id: 5, access: 'DENY', groupId: 3, userGroupSlug: 'core' },
  { id: 6, access: 'ALLOW', groupId: 4, userGroupSlug: 'void' },
  { id: 7, access: null, groupId: 5, userGroupSlug: 'côre' },
];

export const GROUPS: readonly GroupSeed[] = [
  { id: 1, label: 'a', mediaId: 1 },
  { id: 2, label: 'b', mediaId: 1 },
  { id: 3, label: 'c', mediaId: 2 },
  { id: 4, label: 'd', mediaId: 3 },
  { id: 5, label: 'e', mediaId: 4 },
  { id: 6, label: 'f', mediaId: 6 },
];

export const MEDIA: readonly MediaSeed[] = [
  { id: 1, name: 'first' },
  { id: 2, name: 'second' },
  { id: 3, name: 'third' },
  { id: 4, name: 'fourth' },
  { id: 5, name: 'fifth' },
  { id: 6, name: 'sixth' },
  { id: 7, name: 'seventh' },
];

export const STORAGE: readonly StorageSeed[] = [
  { id: 1, userId: 'u1' },
  { id: 2, userId: 'u2' },
];

export const VIDEOS: readonly VideoSeed[] = [
  { id: 1, mediaId: 1, cloudStorageId: 1 },
  { id: 2, mediaId: 1, cloudStorageId: 2 },
  { id: 3, mediaId: 2, cloudStorageId: 1 },
  { id: 4, mediaId: 3, cloudStorageId: null },
  { id: 5, mediaId: 5, cloudStorageId: 1 },
  { id: 6, mediaId: 6, cloudStorageId: 1 },
];

export const LIBRARY_DDL: readonly string[] = [
  `DROP TABLE IF EXISTS "membership_rows"`,
  `DROP TABLE IF EXISTS "policy_rows"`,
  `DROP TABLE IF EXISTS "usergroup_rows"`,
  `DROP TABLE IF EXISTS "group_rows"`,
  `DROP TABLE IF EXISTS "video_rows"`,
  `DROP TABLE IF EXISTS "storage_rows"`,
  `DROP TABLE IF EXISTS "media_rows"`,
  `CREATE TABLE "media_rows" (
     "media_pk" INTEGER PRIMARY KEY,
     "media_name" TEXT NOT NULL
   )`,
  `CREATE TABLE "usergroup_rows" (
     "usergroup_slug" TEXT PRIMARY KEY,
     "usergroup_name" TEXT NOT NULL
   )`,
  `CREATE TABLE "storage_rows" (
     "storage_pk" INTEGER PRIMARY KEY,
     "user_ref" TEXT NOT NULL
   )`,
  `CREATE TABLE "group_rows" (
     "group_pk" INTEGER PRIMARY KEY,
     "group_label" TEXT NOT NULL,
     "media_ref" INTEGER NOT NULL REFERENCES "media_rows"("media_pk")
   )`,
  `CREATE TABLE "policy_rows" (
     "policy_pk" INTEGER PRIMARY KEY,
     "access_value" TEXT,
     "group_ref" INTEGER NOT NULL REFERENCES "group_rows"("group_pk"),
     "usergroup_ref" TEXT REFERENCES "usergroup_rows"("usergroup_slug")
   )`,
  `CREATE TABLE "membership_rows" (
     "membership_pk" INTEGER PRIMARY KEY,
     "user_ref" TEXT NOT NULL,
     "usergroup_ref" TEXT NOT NULL REFERENCES "usergroup_rows"("usergroup_slug")
   )`,
  `CREATE TABLE "video_rows" (
     "video_pk" INTEGER PRIMARY KEY,
     "media_ref" INTEGER NOT NULL REFERENCES "media_rows"("media_pk"),
     "storage_ref" INTEGER REFERENCES "storage_rows"("storage_pk")
   )`,
];

export interface Statement {
  readonly sql: string;
  readonly parameters: readonly SqlParameter[];
}

function insert(
  dialect: SqlDialect,
  table: string,
  columns: readonly string[],
  values: readonly SqlParameter[],
): Statement {
  const quoted = columns.map((column) => dialect.quoteIdentifier(column)).join(', ');
  const placeholders = columns.map((_column, index) => dialect.placeholder(index + 1)).join(', ');
  return {
    sql: `INSERT INTO ${dialect.quoteIdentifier(table)} (${quoted}) VALUES (${placeholders})`,
    parameters: values,
  };
}

export function libraryInserts(dialect: SqlDialect): readonly Statement[] {
  return [
    ...MEDIA.map((seed) =>
      insert(dialect, 'media_rows', ['media_pk', 'media_name'], [seed.id, seed.name]),
    ),
    ...USER_GROUPS.map((seed) =>
      insert(dialect, 'usergroup_rows', ['usergroup_slug', 'usergroup_name'], [seed.slug, seed.name]),
    ),
    ...STORAGE.map((seed) =>
      insert(dialect, 'storage_rows', ['storage_pk', 'user_ref'], [seed.id, seed.userId]),
    ),
    ...GROUPS.map((seed) =>
      insert(
        dialect,
        'group_rows',
        ['group_pk', 'group_label', 'media_ref'],
        [seed.id, seed.label, seed.mediaId],
      ),
    ),
    ...POLICIES.map((seed) =>
      insert(
        dialect,
        'policy_rows',
        ['policy_pk', 'access_value', 'group_ref', 'usergroup_ref'],
        [seed.id, seed.access, seed.groupId, seed.userGroupSlug],
      ),
    ),
    ...MEMBERSHIPS.map((seed) =>
      insert(
        dialect,
        'membership_rows',
        ['membership_pk', 'user_ref', 'usergroup_ref'],
        [seed.id, seed.userId, seed.userGroupSlug],
      ),
    ),
    ...VIDEOS.map((seed) =>
      insert(
        dialect,
        'video_rows',
        ['video_pk', 'media_ref', 'storage_ref'],
        [seed.id, seed.mediaId, seed.cloudStorageId],
      ),
    ),
  ];
}

type Node = Record<string, unknown>;

export const LIBRARY_DEPTH = 10;

function expand(depth: number): { media: Node[]; groups: Node[] } {
  const cache = new Map<string, Node | null>();
  const at = (key: string, make: () => Node | null): Node | null => {
    if (!cache.has(key)) {
      cache.set(key, make());
    }
    return cache.get(key)!;
  };
  const storage = (id: number | null): Node | null => {
    const seed = STORAGE.find((entry) => entry.id === id);
    return seed === undefined ? null : { ...seed };
  };
  const video = (seed: VideoSeed): Node => ({ ...seed, cloudStorage: storage(seed.cloudStorageId) });
  const userGroup = (slug: string | null, level: number): Node | null =>
    at(`ug:${slug}:${level}`, () => {
      const seed = USER_GROUPS.find((entry) => entry.slug === slug);
      if (seed === undefined || level <= 0) {
        return null;
      }
      return {
        ...seed,
        users: MEMBERSHIPS.filter((entry) => entry.userGroupSlug === slug).map((entry) =>
          membership(entry, level - 1),
        ),
        policies: POLICIES.filter((entry) => entry.userGroupSlug === slug).map((entry) =>
          policy(entry, level - 1),
        ),
      };
    });
  const membership = (seed: MembershipSeed, level: number): Node =>
    at(`m:${seed.id}:${level}`, () => ({
      ...seed,
      userGroup: userGroup(seed.userGroupSlug, level - 1),
    }))!;
  const policy = (seed: PolicySeed, level: number): Node =>
    at(`p:${seed.id}:${level}`, () => ({
      ...seed,
      userGroup: userGroup(seed.userGroupSlug, level - 1),
      group: group(GROUPS.find((entry) => entry.id === seed.groupId)!, level - 1),
    }))!;
  const group = (seed: GroupSeed, level: number): Node | null =>
    at(`g:${seed.id}:${level}`, () => {
      if (level <= 0) {
        return null;
      }
      return {
        ...seed,
        media: media(seed.mediaId, level - 1),
        policy: POLICIES.filter((entry) => entry.groupId === seed.id).map((entry) =>
          policy(entry, level - 1),
        ),
      };
    });
  const media = (id: number, level: number): Node | null =>
    at(`media:${id}:${level}`, () => {
      const seed = MEDIA.find((entry) => entry.id === id);
      if (seed === undefined || level <= 0) {
        return null;
      }
      return {
        ...seed,
        groups: GROUPS.filter((entry) => entry.mediaId === id).map(
          (entry) => group(entry, level - 1) as Node,
        ),
        videos: VIDEOS.filter((entry) => entry.mediaId === id).map(video),
      };
    });
  return {
    media: MEDIA.map((seed) => media(seed.id, depth) as Node),
    groups: GROUPS.map((seed) => group(seed, depth) as Node),
  };
}

export function libraryMediaRows(depth: number = LIBRARY_DEPTH): Row[] {
  return expand(depth).media as unknown as Row[];
}

export function libraryGroupRows(depth: number = LIBRARY_DEPTH): Row[] {
  return expand(depth).groups as unknown as Row[];
}

function memberOf(user: string): Record<string, unknown> {
  return { userGroup: { users: { some: { userId: user } } } };
}

function readableBy(user: string): Record<string, unknown> {
  return {
    groups: { some: { policy: { some: { AND: [{ NOT: { access: 'DENY' } }, memberOf(user)] } } } },
  };
}

function notDeniedTo(user: string): Record<string, unknown> {
  return {
    groups: { none: { policy: { some: { AND: [{ access: 'DENY' }, memberOf(user)] } } } },
  };
}

function ownsEveryVideo(user: string): Record<string, unknown> {
  return { videos: { every: { cloudStorage: { userId: user } } } };
}

export const PRODUCTION_RULES: Readonly<Record<string, Record<string, unknown>>> = {
  'production/readable-by-u1': readableBy('u1'),
  'production/readable-by-u2': readableBy('u2'),
  'production/not-denied-to-u1': notDeniedTo('u1'),
  'production/not-denied-to-u2': notDeniedTo('u2'),
  'production/owns-every-video-u1': ownsEveryVideo('u1'),
  'production/owns-every-video-u2': ownsEveryVideo('u2'),
};

const PRODUCTION_CASES: readonly WhereCase[] = Object.entries(PRODUCTION_RULES).map(
  ([id, where]) => ({ id, where }),
);

const QUANTIFIER_CASES: readonly WhereCase[] = [
  { id: 'some/empty', where: { groups: { some: {} } } },
  { id: 'none/empty', where: { groups: { none: {} } } },
  { id: 'every/empty', where: { groups: { every: {} } } },
  { id: 'filter/empty', where: { groups: {} } },
  { id: 'some/label', where: { groups: { some: { label: 'a' } } } },
  { id: 'none/label', where: { groups: { none: { label: 'a' } } } },
  { id: 'every/label', where: { groups: { every: { label: 'a' } } } },
  { id: 'every/label-in', where: { groups: { every: { label: { in: ['a', 'b', 'f'] } } } } },
  { id: 'some+none', where: { groups: { some: { label: 'a' }, none: { label: 'b' } } } },
  { id: 'some+every+none', where: { groups: { some: {}, every: { label: { not: 'c' } }, none: { label: 'e' } } } },
  { id: 'videos/some/empty', where: { videos: { some: {} } } },
  { id: 'videos/none/empty', where: { videos: { none: {} } } },
  { id: 'videos/every/empty', where: { videos: { every: {} } } },
  { id: 'videos/every/storage-null', where: { videos: { every: { cloudStorageId: null } } } },
  { id: 'videos/some/storage-null', where: { videos: { some: { cloudStorageId: null } } } },
  { id: 'videos/every/storage-present', where: { videos: { every: { cloudStorageId: { not: null } } } } },
];

const NULL_LEAF_CASES: readonly WhereCase[] = [
  { id: 'null-leaf/every-access-allow', where: { groups: { every: { policy: { every: { access: 'ALLOW' } } } } } },
  { id: 'null-leaf/every-access-not-deny', where: { groups: { every: { policy: { every: { NOT: { access: 'DENY' } } } } } } },
  { id: 'null-leaf/every-access-null', where: { groups: { every: { policy: { every: { access: null } } } } } },
  { id: 'null-leaf/some-access-null', where: { groups: { some: { policy: { some: { access: null } } } } } },
  { id: 'null-leaf/none-access-null', where: { groups: { none: { policy: { none: { access: null } } } } } },
  { id: 'null-leaf/every-access-in', where: { groups: { every: { policy: { every: { access: { in: ['ALLOW', 'DENY'] } } } } } } },
  { id: 'null-leaf/every-access-notIn', where: { groups: { every: { policy: { every: { access: { notIn: ['DENY'] } } } } } } },
];

const MIXED_DEPTH_CASES: readonly WhereCase[] = [
  {
    id: 'depth/three-many-many-one',
    where: { groups: { some: { policy: { some: { userGroup: { name: 'Core' } } } } } },
  },
  {
    id: 'depth/four-many-many-one-many',
    where: { groups: { some: { policy: { some: { userGroup: { users: { some: { userId: 'u1' } } } } } } } },
  },
  {
    id: 'depth/four-every-at-the-end',
    where: { groups: { some: { policy: { some: { userGroup: { users: { every: { userId: 'u1' } } } } } } } },
  },
  {
    id: 'depth/four-none-at-the-end',
    where: { groups: { every: { policy: { every: { userGroup: { users: { none: { userId: 'u2' } } } } } } } },
  },
  {
    id: 'depth/five-back-through-membership',
    where: {
      groups: {
        some: { policy: { some: { userGroup: { users: { some: { userGroup: { name: 'Core' } } } } } } },
      },
    },
  },
  {
    id: 'depth/six-alternating',
    where: {
      groups: {
        some: {
          policy: {
            some: {
              userGroup: {
                users: { some: { userGroup: { policies: { some: { access: 'DENY' } } } } },
              },
            },
          },
        },
      },
    },
  },
  {
    id: 'depth/six-alternating-every',
    where: {
      groups: {
        some: {
          policy: {
            some: {
              userGroup: {
                users: { some: { userGroup: { policies: { every: { access: 'ALLOW' } } } } },
              },
            },
          },
        },
      },
    },
  },
  {
    id: 'depth/hop-back-to-the-root-model',
    where: { groups: { some: { policy: { some: { group: { media: { name: 'first' } } } } } } },
  },
  {
    id: 'depth/NOT-inside-a-quantifier',
    where: { groups: { some: { NOT: { policy: { some: { access: 'DENY' } } } } } },
  },
  {
    id: 'depth/quantifier-inside-a-NOT',
    where: { NOT: { groups: { some: { policy: { some: { access: 'DENY' } } } } } },
  },
  {
    id: 'depth/sibling-quantifiers',
    where: {
      AND: [
        { groups: { some: { policy: { some: { access: 'ALLOW' } } } } },
        { groups: { none: { policy: { some: { access: 'DENY' } } } } },
      ],
    },
  },
  {
    id: 'depth/some-storage-isNot-owner',
    where: { videos: { some: { cloudStorage: { isNot: { userId: 'u1' } } } } },
  },
  {
    id: 'depth/every-storage-isNot-owner',
    where: { videos: { every: { cloudStorage: { isNot: { userId: 'u2' } } } } },
  },
  {
    id: 'depth/none-storage-isNot-owner',
    where: { videos: { none: { cloudStorage: { isNot: { userId: 'u1' } } } } },
  },
  {
    id: 'depth/some-storage-isNot-null',
    where: { videos: { some: { cloudStorage: { isNot: null } } } },
  },
  {
    id: 'depth/some-storage-is-null',
    where: { videos: { some: { cloudStorage: { is: null } } } },
  },
  {
    id: 'depth/some-storage-bare-null',
    where: { videos: { some: { cloudStorage: null } } },
  },
  {
    id: 'depth/quantifier-beside-a-to-one-chain',
    where: {
      OR: [
        { groups: { some: { policy: { some: { userGroup: { name: 'Empty' } } } } } },
        { videos: { some: { cloudStorage: { userId: 'u2' } } } },
      ],
    },
  },
];

const STRING_KEY_CASES: readonly WhereCase[] = [
  { id: 'string-fk/users-some-lower', where: { groups: { some: { policy: { some: { userGroup: { users: { some: { userId: 'u2' } } } } } } } } },
  { id: 'string-fk/slug-upper', where: { groups: { some: { policy: { some: { userGroup: { slug: 'Core' } } } } } } },
  { id: 'string-fk/slug-accented', where: { groups: { some: { policy: { some: { userGroup: { slug: 'côre' } } } } } } },
  { id: 'string-fk/slug-lt', where: { groups: { some: { policy: { some: { userGroup: { slug: { lt: 'core' } } } } } } } },
  { id: 'string-fk/slug-gte', where: { groups: { some: { policy: { some: { userGroup: { slug: { gte: 'core' } } } } } } } },
  { id: 'string-fk/members-lt', where: { groups: { some: { policy: { some: { userGroup: { users: { some: { userGroupSlug: { lt: 'core' } } } } } } } } } },
  { id: 'string-fk/every-member-slug', where: { groups: { every: { policy: { every: { userGroup: { users: { every: { userGroupSlug: { gte: 'Core' } } } } } } } } } },
  { id: 'string-fk/usergroup-null', where: { groups: { some: { policy: { some: { userGroupSlug: null } } } } } },
  { id: 'string-fk/usergroup-not-null', where: { groups: { every: { policy: { every: { userGroupSlug: { not: null } } } } } } },
];

const EMPTY_BRANCH_CASES: readonly WhereCase[] = [
  { id: 'empty/OR-inside-some', where: { groups: { some: { OR: [] } } } },
  { id: 'empty/OR-inside-none', where: { groups: { none: { OR: [] } } } },
  { id: 'empty/OR-inside-every', where: { groups: { every: { OR: [] } } } },
  { id: 'empty/AND-inside-every', where: { groups: { every: { AND: [] } } } },
  { id: 'empty/object-inside-some', where: { groups: { some: { AND: [{}] } } } },
  { id: 'empty/OR-two-levels-down', where: { groups: { some: { policy: { some: { OR: [] } } } } } },
  { id: 'empty/every-two-levels-down', where: { groups: { every: { policy: { every: { OR: [] } } } } } },
];

export const LIBRARY_BARE_CASES: readonly WhereCase[] = [
  ...PRODUCTION_CASES,
  ...QUANTIFIER_CASES,
  ...NULL_LEAF_CASES,
  ...MIXED_DEPTH_CASES,
  ...STRING_KEY_CASES,
  ...EMPTY_BRANCH_CASES,
];

export const LIBRARY_CASES: readonly WhereCase[] = [
  ...LIBRARY_BARE_CASES,
  ...LIBRARY_BARE_CASES.map((entry) => ({ id: `NOT(${entry.id})`, where: { NOT: entry.where } })),
];

export const GROUP_CASES: readonly WhereCase[] = [
  { id: 'group/every-access-allow', where: { policy: { every: { access: 'ALLOW' } } } },
  { id: 'group/every-access-deny', where: { policy: { every: { access: 'DENY' } } } },
  { id: 'group/some-access-allow', where: { policy: { some: { access: 'ALLOW' } } } },
  { id: 'group/none-access-allow', where: { policy: { none: { access: 'ALLOW' } } } },
  { id: 'group/every-access-null', where: { policy: { every: { access: null } } } },
  { id: 'group/every-access-not-null', where: { policy: { every: { access: { not: null } } } } },
];

export const NAIVE_EVERY_CASE: Record<string, unknown> = { policy: { every: { access: 'ALLOW' } } };
