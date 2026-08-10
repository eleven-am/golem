// Package p7oracle contains the independent P7 provider and social-event
// acceptance oracle.  It deliberately reads provider tables with direct SQL
// and keeps its expected values separate from the production event codec,
// claim SQL renderer, policy evaluator, subscription grouping, and GraphQL
// serialization paths.
package p7oracle
