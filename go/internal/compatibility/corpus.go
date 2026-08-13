package compatibility

// Frozen corpus digests are compiled independently from testdata. Changing a
// corpus file and its expectation is an explicit compatibility review.
const (
	PublicGoAPICorpusSHA256    = "6f41bc3a3b05a05899d4b6ed962902a65ec974f57715970de8324907a1adb2d7"
	GeneratedGoABICorpusSHA256 = "1b3143c256180cccfcadac36a1477e05ba8062078b82f7327b67763acace6b2b"
	GraphQLABICorpusSHA256     = "b307c48c82966aee3bb4643ce239fc5dc6a620edfcd9cb29e2d15132b7bfc4ce"
)

func publicGoAPIPatterns() []string {
	return []string{
		"./embedding", "./events", "./events/nats", "./golem", "./golemtest", "./graphql", "./observe",
		"./provider", "./provider/postgresql", "./provider/sqlite", "./queryplan", "./runtime",
	}
}

// PublicGoAPIPatterns exposes the compatibility diff inventory to the explicit
// corpus freeze command without maintaining a second package list.
func PublicGoAPIPatterns() []string {
	return publicGoAPIPatterns()
}
