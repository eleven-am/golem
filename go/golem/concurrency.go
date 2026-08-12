package golem

import "github.com/eleven-am/golem/go/internal/concurrencyclaim"

// ExistingVersion is a closed, copyable optimistic-concurrency token for an
// operation that requires an existing row. Its zero value and constructor
// values outside the positive int64 range remain invalid until the mutation
// freeze boundary classifies them as BAD_USER_INPUT.
//
// The representation is deliberately private. Application and generated code
// may construct a version claim, retain it, and compare it for equality, but
// cannot inspect or mutate its representation. A claim is never authority;
// selector authorization, the locked row, and compare-and-swap own that proof.
type ExistingVersion concurrencyclaim.ExistingVersion

// ExpectVersion preserves a positive caller-observed model-local version for a
// single-row update or delete. Non-positive inputs become the closed invalid
// zero token so the ordinary mutation freeze boundary can reject them before
// database work without exposing the raw value.
func ExpectVersion(value int64) ExistingVersion {
	return ExistingVersion(concurrencyclaim.NewExistingVersion(value))
}

// ConcurrencyExpectation is the closed upsert expectation: either an absent
// row or one exact existing version. Its zero value is invalid. No exported
// discriminator or fields exist from which application code could construct a
// new expectation state.
type ConcurrencyExpectation concurrencyclaim.ConcurrencyExpectation

// ExpectExisting declares that an upsert may update only the authorized row
// carrying value. Non-positive values become the closed invalid zero state for
// uniform freeze-time BAD_USER_INPUT classification.
func ExpectExisting(value int64) ConcurrencyExpectation {
	return ConcurrencyExpectation(concurrencyclaim.NewExistingExpectation(value))
}

// ExpectAbsent declares that an upsert may create only when no authorized row
// matches its selector. Authorization and non-disclosure remain runtime-owned;
// this value carries no existence-check capability.
func ExpectAbsent() ConcurrencyExpectation {
	return ConcurrencyExpectation(concurrencyclaim.NewAbsentExpectation())
}

// OptimisticConcurrency marks one model-owned, logical int64 scalar as the
// model's explicit concurrency field. Like the other declaration shells, this
// function is statically interpreted and does not construct runtime schema
// state when executed as ordinary Go.
//
// The compiler remains responsible for verifying stored/non-null ownership,
// excluding identity/default/generated/read-only fields, and projecting the
// exact field identity into all downstream contracts.
func OptimisticConcurrency[M any](_ ScalarColumn[M, int64]) ModelOption[M] {
	return modelOption[M]{}
}
