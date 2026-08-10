# P8 compatibility manifest

Status: schema frozen for P8-G implementation. This document does not mark
P8 evidence rows 19 or 20 complete.

## 1. Purpose and trust boundary

The source tree carries one canonical checked development template at
`compatibility/manifest.json`. Every Go release additionally publishes one
canonical materialized compatibility manifest as a release asset. Both use the
same closed, machine-readable format and inventory the release identities that
may affect a source consumer, generated application, GraphQL client, database,
persisted event, operator, or telemetry consumer.

Neither document is self-authenticating. The checked template's exact SHA-256
digest is bound by a separately compiled trusted constant. Release tooling must
first verify that template, copy it, replace only the `release` object, and
canonical-encode the copy as the published release asset. Signed provenance
binds the checked-template digest, materialized-manifest digest, exact tag-target
commit, and every released artifact subject. A digest stored only inside the
JSON would allow an attacker to rewrite both content and claimed digest and is
therefore forbidden.

The materialized release manifest is not written back into the tagged source
tree. A tracked file cannot contain the hash of the commit that contains that
file without a self-reference. The signed tag binds the source commit; the
published manifest names that already-resolved tag target; and provenance binds
the two without pretending the source template embeds its containing commit.

The parser must reject an unknown, missing, duplicate, out-of-order, empty, or
noncanonical field. Canonical bytes use UTF-8 JSON, two-space indentation,
lexicographically ordered closed inventories, no insignificant trailing space,
one final LF, and no absolute path, DSN, host, user, credential, source text,
schema text, SQL, or data value.

## 2. Version 1 JSON shape

The top-level fields, in exact order, are:

```text
formatVersion
module
release
minimumGoVersion
providers
deploymentProfiles
digests
versions
historicalDecode
requiredActions
knownBoundaries
```

`formatVersion` is `1`. `module` is
`github.com/eleven-am/golem/go`.

`release` contains, in order:

```text
development
version
tag
commit
```

A checked development template uses `development: true`, version `devel`, an
empty tag, and forty zeroes for the commit. Release tooling copies the verified
template and changes only this object. The materialized published manifest uses
`development: false`, a canonical `vMAJOR.MINOR.PATCH` version, the exact
`go/vMAJOR.MINOR.PATCH` tag, and the lowercase forty-hex tag-target commit.
Row 22, not the row-20 parser, proves the signed tag, exact copy-and-replace
transition, and artifact provenance.

`providers` is ordered by provider and contains:

```text
provider
minimumVersion
verificationProfiles
```

Only `sqlite` and `postgresql` are valid providers. PostgreSQL verification
profiles are exactly `c` and `linguistic`; SQLite verification profiles are
exactly `file` and `named-shared-memory`. These identities never contain
connection information.

`deploymentProfiles` is the exact sorted inventory
`adapted-multi-process`, `database-backed-single-process`, and
`embedded-single-process`. Deployment topology and provider collation/storage
verification are deliberately separate dimensions.

`digests` contains lowercase SHA-256 digests for these independently produced
canonical inventories:

```text
publicGoAPI
generatedGoABI
graphQLABI
cliJSON
observation
```

Every JSON document emitted for machine consumption starts with an explicit
`formatVersion`. The version/check generation document, migration-new document,
migration-apply document, build-diagnostics document, version document, and
doctor document are version `1`; inspect is version `2`. A consumer must reject
an unknown version before interpreting any later field. Adding an optional
field does not remove that rule: the document version and semantic inventory
jointly determine compatibility.

`versions` contains, in exact order:

```text
generator
generatedTemplateABI
schemaBundle
generatedManifest
graphQL
modelIR
contractIR
canonicalIR
physicalSchema
physicalCanonical
migrationManifest
migrationCanonical
migrationLedger
eventSchema
factCodecs
eventCodecs
principalSnapshotCodecs
```

Numeric versions are positive integers. Codec inventories are sorted closed
identities. The first release has no persisted principal-snapshot codec, so
`principalSnapshotCodecs` is an empty array; principal snapshots are
application-owned immutable in-memory values, not bytes Golem may reinterpret.

`historicalDecode` contains sorted supported historical versions for
`schemaBundles`, `generatedManifests`, `modelIR`, `contractIR`, `canonicalIR`, `physicalSchema`,
`physicalCanonical`, `migrationManifest`, `migrationCanonical`, and
`migrationLedger`, followed by supported `factCodecs`, `eventCodecs`, and
`principalSnapshotCodecs`. Every current identity must appear in its historical
inventory. An identity may be removed only as a breaking major change with an
executable migration path; unsupported bytes are always refused rather than
guessed.

The first release lists generated-manifest versions `1` and `2`, and contract
IR versions `4` and `5`. Contract v4 is accepted only for an explicitly
supplied historical event bundle: its original canonical bytes and fingerprint
are verified before the absent v5 `hookOwnedCreateFields` inventory is set to
empty. Active bundles remain exact-current-version only.

`requiredActions` and `knownBoundaries` are sorted arrays of closed identifiers.
They contain no prose. Human release notes map those identifiers to guidance.

## 3. Semantic comparison

The comparer receives trusted canonical manifests and the independently frozen
inventories named by their digests. It does not accept a release author's
declared compatibility level as evidence.

Patch permits only release version/tag/commit changes and compatible provider
patch-floor changes. Every API, generated, GraphQL, CLI, observation, schema,
migration, and codec identity must remain compatible and no required action may
be introduced.

Minor permits additive public Go and GraphQL surface changes, additive closed
observation values, new optional CLI JSON fields under a bumped compatible
document version, new provider profiles, new decoders, and new codecs while all
old decoders remain. It may require explicit regeneration, migration, or
operator actions. It may not remove or reinterpret an existing source,
generated, GraphQL, persisted, or machine-output identity.

Major permits a detected breaking change only when the manifest carries the
corresponding closed required-action identity and the release contains the
executable migration guide proved by rows 19, 20, and 22. A major number alone
does not make silent reinterpretation valid.

The semantic gates classify every layer independently and report only stable
layer/reason identifiers. They never include source, GraphQL, SQL, persisted
bytes, paths, or data values in diagnostics.

## 4. Frozen corpora

Compatibility corpus bytes live under `go/internal/compatibility/testdata`.
Each corpus has an independently compiled SHA-256 expectation. Tests first
verify exact bytes, then load them through public tools or strict decoders.
Updating a corpus is an explicit release review; a test regeneration flag may
not silently bless changed bytes.

Row 19 owns source, generated, schema, reviewed migration, exact scalar data,
identity, pending/delivered event, and authorization-behavior upgrade corpora.
Row 20 owns public Go, generated Go, GraphQL, CLI JSON, observation, and format
inventories plus patch/minor/major fixtures. Row 15 consumes the completed row-20
parser to prove that a stale or tampered compatibility manifest is rejected
before database or worker work.
