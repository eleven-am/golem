package ir

import (
	"fmt"
	"github.com/eleven-am/golem/go/internal/dataincarnation"
)

type DataIncarnationContractIR struct {
	Kind    string `json:"kind"`
	Version uint16 `json:"version"`
}

func ValidateDataIncarnationContract(contract ContractIR) error {
	if value := contract.DataIncarnation; value != nil && (value.Kind != "provider" || value.Version != dataincarnation.Version) {
		return fmt.Errorf("data incarnation contract must be provider version 1")
	}
	return nil
}

// ModelDataIncarnation validates the closed schema-owned extension inventory.
func ModelDataIncarnation(model ModelIR) (bool, error) {
	providers := make(map[Provider]bool, len(model.Providers))
	for _, provider := range model.Providers {
		providers[provider] = true
	}
	seen := map[Provider]bool{}
	for _, extension := range model.Extensions {
		if extension.Kind != dataincarnation.Kind {
			continue
		}
		if extension.Version != dataincarnation.Version || extension.Payload != "{}" || extension.Owner != ObjectID(model.Schema.ID) || !providers[extension.Provider] || seen[extension.Provider] {
			return false, fmt.Errorf("data incarnation requires one exact schema-owned extension per declared provider")
		}
		identity, _ := NewIDRegistry().Register(ObjectExtension, OwnedIdentity(string(model.Schema.ID), dataincarnation.Kind+"\x00"+string(extension.Provider)), SourceSpan{})
		if extension.ID != ExtensionIDFrom(identity) {
			return false, fmt.Errorf("data incarnation extension identity is invalid")
		}
		seen[extension.Provider] = true
	}
	if len(seen) != 0 && len(seen) != len(providers) {
		return false, fmt.Errorf("data incarnation provider inventory is incomplete")
	}
	return len(seen) != 0, nil
}

func ValidateDataIncarnation(compilation CompilationIR) error {
	if err := ValidateDataIncarnationContract(compilation.Contract); err != nil {
		return err
	}
	enabled, err := ModelDataIncarnation(compilation.Model)
	if err != nil {
		return err
	}
	if enabled != (compilation.Contract.DataIncarnation != nil) {
		return fmt.Errorf("data incarnation model and contract disagree")
	}
	return nil
}
