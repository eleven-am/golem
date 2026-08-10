// Package phase0 is the historical Phase 0 semantic spike.
//
// It is retained only as an independent ordered-policy oracle and source-history
// fixture. It is not the production Golem API or runtime. In particular, its
// string model/field identities, any-valued records, simplified operator set,
// and in-memory evaluator must not be imported by production packages or copied
// into later phases. Production policy code lives under go/golem and
// go/internal/policy and is governed by docs/golem-go/BIBLE.md.
package phase0
