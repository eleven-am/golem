package compatibility

// Frozen corpus digests are compiled independently from testdata. Changing a
// corpus file and its expectation is an explicit compatibility review.
const (
	PublicGoAPICorpusSHA256    = "1d78abb94828c891f1890b426e71ec9422f43691090d4c14cbb7e8a80a1e6f42"
	GeneratedGoABICorpusSHA256 = "e56cbc8bfc4e1228832d167f2d8cf65aa300555c44708e958c936e23dc58b1f3"
	GraphQLABICorpusSHA256     = "66756b950116b082ed803fe0c17c7f53628425090b8278c84fe5cd4f3615e2c6"
)
