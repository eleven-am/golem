package compatibility

// Frozen corpus digests are compiled independently from testdata. Changing a
// corpus file and its expectation is an explicit compatibility review.
const (
	PublicGoAPICorpusSHA256    = "3657edf04c74943c890103480bcf2a0a775f934b3d2b1bcaf6128a5ea4a99985"
	GeneratedGoABICorpusSHA256 = "c4a84cc2732a0031679b172b967c860388f24215f9c66bb40ed2e2e2d945e14f"
	GraphQLABICorpusSHA256     = "66756b950116b082ed803fe0c17c7f53628425090b8278c84fe5cd4f3615e2c6"
)
