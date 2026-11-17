package ir

const (
	BaseClientManifestVersion   = 1
	ObjectClientManifestVersion = 2
	PageClientManifestVersion   = 3
)

// RequiredClientManifestVersion is shared by manifest emission and the runtime
// handshake. Existing schemas retain format 1; named transport types require 2;
// continuation, declared invalidation or durable data identity requires format 3.
func (contract ContractIR) RequiredClientManifestVersion() int {
	if contract.DataIncarnation != nil {
		return PageClientManifestVersion
	}
	for _, operation := range contract.CustomOperations {
		if operation.Pagination != nil || operation.Invalidation != nil {
			return PageClientManifestVersion
		}
	}
	if len(contract.GraphQLTypes) != 0 {
		return ObjectClientManifestVersion
	}
	return BaseClientManifestVersion
}
