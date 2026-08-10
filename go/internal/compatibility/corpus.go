package compatibility

// Frozen corpus digests are compiled independently from testdata. Changing a
// corpus file and its expectation is an explicit compatibility review.
const (
	PublicGoAPICorpusSHA256    = "e109d8f5a7c70cb66b5e05830b32d695a49553bad1209bb23b5cefb422bf0aeb"
	GeneratedGoABICorpusSHA256 = "c4a84cc2732a0031679b172b967c860388f24215f9c66bb40ed2e2e2d945e14f"
	GraphQLABICorpusSHA256     = "66756b950116b082ed803fe0c17c7f53628425090b8278c84fe5cd4f3615e2c6"
)
