# @eleven-am/golem-policy

Golem's own evaluator for CASL rule conditions, replacing the `@casl/prisma` interpreter.

One operator table defines every supported operator once: its JavaScript predicate, its parameterised SQL rendering, its null semantics, and its operand constraints. Both backends are generated from the same entry, so the in-memory check and the database filter cannot drift apart. Only the JavaScript side is wired up today; the SQL renderings are declarative and dialect-neutral until a SQL backend consumes them.

Conditions are two-valued: an object matches a condition or it does not, and there is no third "unknown" outcome. Null and absent values are ordinary values that fail to equal things, so `AND`, `OR` and `NOT` are classical boolean algebra over the leaves. Numeric comparison is exact across `number`, `bigint` and `Decimal`, including magnitudes past `Number.MAX_SAFE_INTEGER`.

Unsupported operators are rejected, never approximated. `validateConditions` reports every offending operator with its path so an application can refuse a policy at boot instead of silently under- or over-granting at request time.

```ts
import { compileConditions, validateConditions } from '@eleven-am/golem-policy';

const result = validateConditions({ title: { contains: 'x' } });
if (!result.supported) {
  throw new Error(result.issues[0].message);
}

const compiled = compileConditions({ post: { is: { authorId: 'u1' } } });
compiled.test({ post: { authorId: 'u1' } });
```

See the [full guide](https://github.com/eleven-am/golem#readme).
