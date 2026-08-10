import { context } from './analytics';
import { ScopedQuery } from '../../src/scoped';
import { ScopedFieldSpec, scopedFieldEngine, scopedFieldQuery } from './fixture';

export interface FieldScopeTarget {
  readonly provider: string;
  readonly client: Record<string, any>;
}

const VISIBLE_WHEN_PUBLISHED: readonly ScopedFieldSpec[] = [
  { model: 'Post', field: 'secretNote', condition: { published: true } },
];

export function fieldScopeSuite(target: () => FieldScopeTarget): void {
  const rooted = (fields: readonly ScopedFieldSpec[]) =>
    scopedFieldQuery(
      {
        provider: target().provider,
        client: target().client,
        constraints: { Post: { authorId: 1 } },
        fields,
        context,
      },
      { model: 'Post', context },
    );

  describe('a field policy over a scoped query against a live database', () => {
    it('hands back the value only for the rows whose condition holds', async () => {
      const rows = await rooted(VISIBLE_WHEN_PUBLISHED)
        .query((qb: any) => qb.select(['Post.id', 'Post.secretNote']).orderBy('Post.id'))
        .execute();

      expect(rows).toEqual([
        { id: 1, secretNote: 'n1' },
        { id: 2, secretNote: 'n2' },
        { id: 3, secretNote: null },
      ]);
    });

    it('cannot recover a withheld value by filtering for it', async () => {
      const rows = await rooted(VISIBLE_WHEN_PUBLISHED)
        .query((qb: any) => qb.select('Post.id').where('Post.secretNote', '=', 'n3'))
        .execute();

      expect(rows).toEqual([]);
    });

    it('still filters on the values the caller may read', async () => {
      const rows = await rooted(VISIBLE_WHEN_PUBLISHED)
        .query((qb: any) => qb.select('Post.id').where('Post.secretNote', '=', 'n1'))
        .execute();

      expect(rows).toEqual([{ id: 1 }]);
    });

    it('cannot order the withheld value into view', async () => {
      const rows = await rooted(VISIBLE_WHEN_PUBLISHED)
        .query((qb: any) => qb.select(['Post.id', 'Post.secretNote']).orderBy('Post.secretNote'))
        .execute();

      expect(rows.map((row: any) => row.secretNote).filter(Boolean).sort()).toEqual(['n1', 'n2']);
    });

    it('refuses a query that reaches a field the caller may never read', async () => {
      await expect(
        rooted([{ model: 'Post', field: 'secretNote', access: 'never' }])
          .query((qb: any) => qb.select('Post.id').where('Post.secretNote', '=', 'n3'))
          .execute(),
      ).rejects.toThrow('may not reference "Post"."secretNote"');
    });
  });
}

const NEVER_READABLE: readonly ScopedFieldSpec[] = [
  { model: 'Post', field: 'secretNote', access: 'never' },
];

export function engineFieldScopeSuite(target: () => FieldScopeTarget): void {
  const rooted = (fields: readonly ScopedFieldSpec[]): ScopedQuery =>
    scopedFieldEngine({
      provider: target().provider,
      client: target().client,
      constraints: { Post: { authorId: 1 } },
      fields,
    }).scoped({ model: 'Post', context });

  describe('a field policy the engine hands its own scoped query', () => {
    it('refuses to select a field the caller may never read', async () => {
      await expect(
        rooted(NEVER_READABLE)
          .query((qb: any) => qb.select(['Post.id', 'Post.secretNote']))
          .execute(),
      ).rejects.toThrow('may not reference "Post"."secretNote"');
    });

    it('refuses to filter by a field the caller may never read', async () => {
      await expect(
        rooted(NEVER_READABLE)
          .query((qb: any) => qb.select('Post.id').where('Post.secretNote', '=', 'n3'))
          .execute(),
      ).rejects.toThrow('may not reference "Post"."secretNote"');
    });

    it('refuses to order by a field the caller may never read', async () => {
      await expect(
        rooted(NEVER_READABLE)
          .query((qb: any) => qb.select('Post.id').orderBy('Post.secretNote'))
          .execute(),
      ).rejects.toThrow('may not reference "Post"."secretNote"');
    });

    it('keeps a field the caller may never read out of a star projection', async () => {
      const rows = await rooted(NEVER_READABLE)
        .query((qb: any) => qb.selectAll().orderBy('Post.id'))
        .execute();

      expect(rows).toHaveLength(3);
      for (const row of rows as Record<string, unknown>[]) {
        expect(Object.keys(row)).not.toContain('secretNote');
        expect(Object.keys(row)).toContain('title');
      }
    });

    it('projects a conditionally readable field as its mask', async () => {
      const rows = await rooted(VISIBLE_WHEN_PUBLISHED)
        .query((qb: any) => qb.select(['Post.id', 'Post.secretNote']).orderBy('Post.id'))
        .execute();

      expect(rows).toEqual([
        { id: 1, secretNote: 'n1' },
        { id: 2, secretNote: 'n2' },
        { id: 3, secretNote: null },
      ]);
    });

    it('cannot recover a masked value by filtering for it', async () => {
      const rows = await rooted(VISIBLE_WHEN_PUBLISHED)
        .query((qb: any) => qb.select('Post.id').where('Post.secretNote', '=', 'n3'))
        .execute();

      expect(rows).toEqual([]);
    });

    it('leaves a field the caller may always read alone', async () => {
      const rows = await rooted(VISIBLE_WHEN_PUBLISHED)
        .query((qb: any) =>
          qb.select(['Post.id', 'Post.title']).where('Post.title', '=', 'a3').orderBy('Post.id'),
        )
        .execute();

      expect(rows).toEqual([{ id: 3, title: 'a3' }]);
    });
  });
}
