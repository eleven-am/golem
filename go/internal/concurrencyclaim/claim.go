// Package concurrencyclaim owns the primitive, authority-free representation
// of optimistic-concurrency claims shared by the public construction facade
// and the mutation runtime.
package concurrencyclaim

// ExistingVersion is the internal representation of a claim that an
// authorized existing row has one exact positive version.
//
// It intentionally contains only a primitive comparable value. The zero value
// is invalid and is the canonical representation for every rejected input.
type ExistingVersion struct {
	value int64
}

// NewExistingVersion constructs the canonical internal representation. Values
// outside the positive int64 range collapse to the invalid zero value.
func NewExistingVersion(value int64) ExistingVersion {
	if value <= 0 {
		return ExistingVersion{}
	}
	return ExistingVersion{value: value}
}

// InspectExistingVersion returns the claimed version only when claim is the
// canonical positive representation.
func InspectExistingVersion(claim ExistingVersion) (int64, bool) {
	if claim.value <= 0 {
		return 0, false
	}
	return claim.value, true
}

// ValidExistingVersion reports only canonical validity. Mutation runtime uses
// this at the pre-database freeze boundary; it must not recover the caller's
// raw value for a SQL bind.
func ValidExistingVersion(claim ExistingVersion) bool {
	return claim.value > 0
}

type expectationState uint8

const (
	expectationAbsent expectationState = iota + 1
	expectationExisting
)

// ConcurrencyExpectation is the internal representation of an upsert claim.
// It is either the one absent state or one exact positive existing version.
// Its zero value and all noncanonical combinations are invalid.
type ConcurrencyExpectation struct {
	state   expectationState
	version int64
}

// NewAbsentExpectation constructs the one valid absent-row representation.
func NewAbsentExpectation() ConcurrencyExpectation {
	return ConcurrencyExpectation{state: expectationAbsent}
}

// NewExistingExpectation constructs an existing-row representation for a
// positive version. Rejected inputs collapse to the invalid zero value.
func NewExistingExpectation(value int64) ConcurrencyExpectation {
	claim := NewExistingVersion(value)
	version, valid := InspectExistingVersion(claim)
	if !valid {
		return ConcurrencyExpectation{}
	}
	return ConcurrencyExpectation{state: expectationExisting, version: version}
}

// IsAbsent reports whether expectation is exactly the canonical absent state.
func IsAbsent(expectation ConcurrencyExpectation) bool {
	return expectation.state == expectationAbsent && expectation.version == 0
}

// InspectExistingExpectation returns the exact version only for a canonical
// existing-row representation.
func InspectExistingExpectation(expectation ConcurrencyExpectation) (int64, bool) {
	if expectation.state != expectationExisting || expectation.version <= 0 {
		return 0, false
	}
	return expectation.version, true
}

// ValidExpectation reports whether expectation is one of the two canonical
// states accepted by the mutation runtime.
func ValidExpectation(expectation ConcurrencyExpectation) bool {
	if IsAbsent(expectation) {
		return true
	}
	_, valid := InspectExistingExpectation(expectation)
	return valid
}
