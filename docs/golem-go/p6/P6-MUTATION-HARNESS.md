# P6 named-mutation harness

P6-I uses an executable mutation catalog, not a manual code-review claim. The
harness copies the `go` module to a fresh temporary directory, validates that
each source replacement has exactly one match, applies the mutation only in
that copy, and runs the exact named test attached to the ledger label.

From the repository's `go` directory:

```text
go run ./internal/cmd/p6mutation -list
go run ./internal/cmd/p6mutation -labels DECIMAL_TO_REAL,DROP_ORDER_TIEBREAK
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go run ./internal/cmd/p6mutation
```

A mutation is `KILLED` only when `go test` reports the required named test as
failed. A package compile failure is `INVALID`, not a kill. A green named test
is `SURVIVED`. Missing provider environment is `SKIPPED` and makes a requested
verification run fail. Mutant tests use `-vet=off` because P6's separate final
command runs `go vet ./...`; this keeps the 25 isolated recompilations bounded
without weakening any named runtime or compile oracle.

`-list` always shows the complete immutable label inventory from
`P6-EVIDENCE.md`. Labels whose alternate implementation is not encoded remain
visible as `REMAINING`. The default execution includes every catalog label, so
an uncovered label becomes `SKIPPED` and the command fails. Development runs
may explicitly use `-allow-uncovered`; that flag is forbidden in P6 completion
evidence. The ledger may move a mutation to complete only after that exact
label is covered and recorded as killed on the required provider profiles.

Use `-keep` only for debugging. It prints the temporary module directory; the
normal mode removes every isolated copy after its result.
