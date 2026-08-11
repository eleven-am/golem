package compatibility

// Frozen corpus digests are compiled independently from testdata. Changing a
// corpus file and its expectation is an explicit compatibility review.
const (
	PublicGoAPICorpusSHA256    = "52d7310afcf57870a401868f8ee2a56a7fd0b8543d34748f73b5590829880b2f"
	GeneratedGoABICorpusSHA256 = "c4a84cc2732a0031679b172b967c860388f24215f9c66bb40ed2e2e2d945e14f"
	GraphQLABICorpusSHA256     = "66756b950116b082ed803fe0c17c7f53628425090b8278c84fe5cd4f3615e2c6"
)
