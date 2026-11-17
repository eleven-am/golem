// Package invalidationcontract owns the shared custom-invalidation wire vocabulary and declaration bounds.
package invalidationcontract

const (
	Capability      = "operation-invalidation-v1"
	ReadableRows    = "readableRows"
	PolicyFacts     = "policyFacts"
	MaxDependencies = 32
	MaxMembers      = 64
)
