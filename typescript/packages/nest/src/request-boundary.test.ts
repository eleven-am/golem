import {
  SHARED_CONTEXT_MESSAGE,
  assertContextWithinRequest,
  golemRequestBoundary,
  golemSharedContext,
} from './request-boundary';

function inRequest<T>(fn: () => T): T {
  let result!: T;
  golemRequestBoundary({}, {}, () => {
    result = fn();
  });
  return result;
}

describe('request boundary guard', () => {
  it('does nothing outside any request boundary', () => {
    const ctx = {};

    expect(() => {
      assertContextWithinRequest(ctx);
      assertContextWithinRequest(ctx);
    }).not.toThrow();
  });

  it('ignores a non-object context', () => {
    expect(() =>
      inRequest(() => {
        assertContextWithinRequest(null);
        assertContextWithinRequest(undefined);
        assertContextWithinRequest('ctx');
      }),
    ).not.toThrow();
  });

  it('accepts many uses of one context inside one request', () => {
    const ctx = {};

    expect(() =>
      inRequest(() => {
        assertContextWithinRequest(ctx);
        assertContextWithinRequest(ctx);
        assertContextWithinRequest(ctx);
      }),
    ).not.toThrow();
  });

  it('accepts a fresh context per request', () => {
    inRequest(() => assertContextWithinRequest({}));

    expect(() => inRequest(() => assertContextWithinRequest({}))).not.toThrow();
  });

  it('refuses one context seen across two requests', () => {
    const ctx = {};
    inRequest(() => assertContextWithinRequest(ctx));

    expect(() => inRequest(() => assertContextWithinRequest(ctx))).toThrow(
      SHARED_CONTEXT_MESSAGE,
    );
  });

  it('names the misconfiguration and the fix', () => {
    expect(SHARED_CONTEXT_MESSAGE).toContain('static context object');
    expect(SHARED_CONTEXT_MESSAGE).toContain('context: ({ req }) => ({ req })');
    expect(SHARED_CONTEXT_MESSAGE).toContain('[golemSharedContext]: true');
  });

  it('keeps refusing on every later request', () => {
    const ctx = {};
    inRequest(() => assertContextWithinRequest(ctx));

    expect(() => inRequest(() => assertContextWithinRequest(ctx))).toThrow();
    expect(() => inRequest(() => assertContextWithinRequest(ctx))).toThrow();
  });

  it('refuses a reused context even when the requests interleave', async () => {
    const ctx = {};
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });

    const first = new Promise<void>((resolve, reject) => {
      golemRequestBoundary({}, {}, () => {
        void gate.then(() => {
          try {
            assertContextWithinRequest(ctx);
            resolve();
          } catch (error) {
            reject(error as Error);
          }
        });
      });
    });
    const second = inRequest(() => {
      assertContextWithinRequest(ctx);
      release();
      return first;
    });

    await expect(second).rejects.toThrow(SHARED_CONTEXT_MESSAGE);
  });

  it('follows the request across awaits', async () => {
    const ctx = {};
    let settle!: () => void;
    const later = new Promise<void>((resolve) => {
      settle = resolve;
    });
    const checked = new Promise<void>((resolve, reject) => {
      golemRequestBoundary({}, {}, () => {
        void later.then(() => {
          try {
            assertContextWithinRequest(ctx);
            assertContextWithinRequest(ctx);
            resolve();
          } catch (error) {
            reject(error as Error);
          }
        });
      });
    });

    settle();

    await expect(checked).resolves.toBeUndefined();
  });

  it('leaves a context that declares itself deliberately shared', () => {
    const ctx = { [golemSharedContext]: true };
    inRequest(() => assertContextWithinRequest(ctx));

    expect(() => inRequest(() => assertContextWithinRequest(ctx))).not.toThrow();
  });

  it('refuses a fresh context carrying the req of an earlier request', () => {
    const req = {};
    golemRequestBoundary(req, {}, () => assertContextWithinRequest({ req }));

    expect(() =>
      golemRequestBoundary({}, {}, () => assertContextWithinRequest({ req })),
    ).toThrow(SHARED_CONTEXT_MESSAGE);
  });

  it('sees the raw request through a framework wrapper', () => {
    const raw = {};
    golemRequestBoundary(raw, {}, () => assertContextWithinRequest({ req: { raw } }));

    expect(() =>
      golemRequestBoundary({}, {}, () => assertContextWithinRequest({ req: { raw } })),
    ).toThrow(SHARED_CONTEXT_MESSAGE);
  });

  it('accepts a context carrying the req of the request being served', () => {
    const req = {};

    expect(() =>
      golemRequestBoundary(req, {}, () => {
        assertContextWithinRequest({ req });
        assertContextWithinRequest({ req });
      }),
    ).not.toThrow();
  });

  it('passes no judgement on a req object it never stamped', () => {
    const foreign = {};
    golemRequestBoundary({}, {}, () => assertContextWithinRequest({ req: foreign }));

    expect(() =>
      golemRequestBoundary({}, {}, () => assertContextWithinRequest({ req: foreign })),
    ).not.toThrow();
  });

  it('leaves a deliberately shared context even when it carries a stale req', () => {
    const req = {};
    golemRequestBoundary(req, {}, () => assertContextWithinRequest({ req }));

    expect(() =>
      golemRequestBoundary({}, {}, () =>
        assertContextWithinRequest({ req, [golemSharedContext]: true }),
      ),
    ).not.toThrow();
  });
});
