package oracle

import "github.com/eleven-am/golem/go/internal/policy/ir"

// Run the existing independent scalar, list, JSON and relation corpus through
// fact boundaries too. These remain variants; the primary operator inventory
// and all of its mutation witnesses are unchanged.
func withFactProbes(corpus Corpus) Corpus {
	original := corpus.probes
	corpus.probes = append([]Probe(nil), original...)
	for _, probe := range original {
		root := probe.condition.ModelID()
		id := must(ir.FactIdentity(root, "oracle/"+probe.name))
		scope := must(ir.NewFactScope(must(ir.NewConstant(root, true))))
		fact := must(ir.NewFact(root, id, probe.condition, scope))
		nestedID := must(ir.FactIdentity(root, "nested/"+probe.name))
		nested := must(ir.NewFact(root, nestedID, fact, scope))
		corpus.probes = append(corpus.probes,
			variantProbe("fact/"+probe.name, probe.operatorID, fact),
			variantProbe("nested-fact/"+probe.name, probe.operatorID, nested),
			variantProbe("not-fact/"+probe.name, probe.operatorID, mustLogical(root, ir.LogicalNot, fact)),
		)
		if probe.condition.Kind() == ir.ConditionRelation {
			if authority, err := ir.NewFactScope(probe.condition); err == nil {
				gate := must(ir.NewFactScopeCondition(authority))
				corpus.probes = append(corpus.probes, variantProbe("authority/"+probe.name, probe.operatorID, gate))
			}
		}
	}
	return corpus
}
