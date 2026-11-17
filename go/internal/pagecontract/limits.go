// Package pagecontract owns the protocol ceilings shared by page declaration,
// immutable resolver-batch construction, and continuation admission. Operation
// declarations may impose tighter budgets.
package pagecontract

const (
	MaxItems           = 1000
	MaxCandidates      = 10000
	MaxCheckpointBytes = 2048
)
