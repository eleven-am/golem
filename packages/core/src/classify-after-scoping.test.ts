import { AuthorizationProvider, FieldClassification } from './authorization';
import { GolemValidationError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const models = [
  {
    name: 'ReadingSession',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'articleId', type: 'String' }),
      field({ name: 'durationSec', type: 'Int' }),
      field({ name: 'progressEnd', type: 'Float' }),
      field({ name: 'sessionType', type: 'String' }),
    ],
  },
  {
    name: 'ReadingStat',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'userId', type: 'String' }),
      field({ name: 'date', type: 'String' }),
      field({ name: 'wordsRead', type: 'Int' }),
      field({ name: 'secretFlagged', type: 'Int' }),
    ],
  },
];

const ctx = { req: {} };

function fakeClient() {
  return {
    readingSession: {
      aggregate: jest.fn().mockResolvedValue({ _max: { progressEnd: null } }),
      groupBy: jest.fn().mockResolvedValue([]),
      count: jest.fn().mockResolvedValue(0),
    },
    readingStat: {
      aggregate: jest.fn().mockResolvedValue({}),
      groupBy: jest.fn().mockResolvedValue([]),
      count: jest.fn().mockResolvedValue(0),
    },
  };
}

function provider(
  constraint: unknown,
  classification: Record<string, FieldClassification>,
): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => constraint),
    checkField: jest.fn(async () => true),
    classifyFields: jest.fn(async (_action, _model, fields: readonly string[]) =>
      Object.fromEntries(
        fields.map((name) => [name, classification[name] ?? { access: 'never' }]),
      ),
    ),
  };
}

function engineWith(
  client: ReturnType<typeof fakeClient>,
  authorization: AuthorizationProvider,
): GolemEngine {
  return new GolemEngine(client, models, {
    authorization,
    checkReadFields: true,
    checkWriteResults: false,
  });
}

const ownerScoped: FieldClassification = {
  access: 'conditional',
  requires: ['userId'],
  dischargedByConstraint: true,
};

const relationScoped: FieldClassification = {
  access: 'conditional',
  requires: ['article'],
  dischargedByConstraint: true,
};

const fieldConditional: FieldClassification = {
  access: 'conditional',
  requires: ['flag'],
  dischargedByConstraint: false,
};

describe('R3 — field classification runs after scoping', () => {
  describe('owner-column scoped grant', () => {
    it('aggregates a field the merged constraint makes readable on every row', async () => {
      const client = fakeClient();
      const engine = engineWith(
        client,
        provider({ userId: 'user-1' }, { wordsRead: ownerScoped }),
      );

      await engine.aggregate({
        model: 'ReadingStat',
        _sum: { wordsRead: true },
        context: ctx,
      });

      expect(client.readingStat.aggregate).toHaveBeenCalledWith(
        expect.objectContaining({ where: { userId: 'user-1' } }),
      );
    });

    it('groups by a field the merged constraint makes readable on every row', async () => {
      const client = fakeClient();
      const engine = engineWith(
        client,
        provider(
          { userId: 'user-1' },
          { date: ownerScoped, wordsRead: ownerScoped },
        ),
      );

      await engine.groupBy({
        model: 'ReadingStat',
        by: ['date'],
        _sum: { wordsRead: true },
        context: ctx,
      });

      expect(client.readingStat.groupBy).toHaveBeenCalledWith(
        expect.objectContaining({
          by: ['date'],
          where: { userId: 'user-1' },
        }),
      );
    });
  });

  describe('relation-scoped grant, where the model has no owner column', () => {
    it('aggregates through a constraint that only exists via the relation', async () => {
      const client = fakeClient();
      const engine = engineWith(
        client,
        provider(
          { article: { is: { userId: 'user-1' } } },
          { progressEnd: relationScoped, articleId: { access: 'always' } },
        ),
      );

      await engine.aggregate({
        model: 'ReadingSession',
        where: { articleId: 'article-1' },
        _max: { progressEnd: true },
        context: ctx,
      });

      expect(client.readingSession.aggregate).toHaveBeenCalledWith(
        expect.objectContaining({
          where: {
            AND: [
              { articleId: 'article-1' },
              { article: { is: { userId: 'user-1' } } },
            ],
          },
        }),
      );
    });

    it('groups through a relation-only constraint', async () => {
      const client = fakeClient();
      const engine = engineWith(
        client,
        provider(
          { article: { is: { userId: 'user-1' } } },
          { sessionType: relationScoped, durationSec: relationScoped },
        ),
      );

      await engine.groupBy({
        model: 'ReadingSession',
        by: ['sessionType'],
        _sum: { durationSec: true },
        context: ctx,
      });

      expect(client.readingSession.groupBy).toHaveBeenCalledWith(
        expect.objectContaining({ by: ['sessionType'] }),
      );
    });
  });

  describe('stays fail-closed for conditions the merged constraint does not discharge', () => {
    it('refuses a measure whose condition is field-specific', async () => {
      const engine = engineWith(
        fakeClient(),
        provider({ userId: 'user-1' }, { secretFlagged: fieldConditional }),
      );

      await expect(
        engine.aggregate({
          model: 'ReadingStat',
          _sum: { secretFlagged: true },
          context: ctx,
        }),
      ).rejects.toThrow(GolemValidationError);
    });

    it('names the field and the undischarged condition', async () => {
      const engine = engineWith(
        fakeClient(),
        provider({ userId: 'user-1' }, { secretFlagged: fieldConditional }),
      );

      await expect(
        engine.aggregate({
          model: 'ReadingStat',
          _sum: { secretFlagged: true },
          context: ctx,
        }),
      ).rejects.toThrow(/secretFlagged[\s\S]*flag/);
    });

    it('refuses a grouping key whose condition is field-specific', async () => {
      const engine = engineWith(
        fakeClient(),
        provider(
          { userId: 'user-1' },
          { secretFlagged: fieldConditional, wordsRead: ownerScoped },
        ),
      );

      await expect(
        engine.groupBy({
          model: 'ReadingStat',
          by: ['secretFlagged'],
          _sum: { wordsRead: true },
          context: ctx,
        }),
      ).rejects.toThrow(GolemValidationError);
    });

    it('refuses a never-readable field even under a scoped grant', async () => {
      const engine = engineWith(
        fakeClient(),
        provider({ userId: 'user-1' }, { wordsRead: { access: 'never' } }),
      );

      await expect(
        engine.aggregate({
          model: 'ReadingStat',
          _sum: { wordsRead: true },
          context: ctx,
        }),
      ).rejects.toThrow(GolemValidationError);
    });
  });
});
