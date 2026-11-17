package schema

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/physical"
)

func incarnationBundle(t *testing.T, modelEnabled, contractEnabled, physicalEnabled bool) golem.SchemaBundle {
	t.Helper()
	base, _ := testBundle(t)
	var model ir.ModelIR
	var contract ir.ContractIR
	if err := decodeCanonicalJSON(base.Model().Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if err := decodeCanonicalJSON(base.Contract().Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	if modelEnabled {
		model.Schema.ID = ir.SchemaID(testID(90))
		for _, provider := range model.Providers {
			identity, diagnostic := ir.NewIDRegistry().Register(ir.ObjectExtension, ir.OwnedIdentity(string(model.Schema.ID), dataincarnation.Kind+"\x00"+string(provider)), ir.SourceSpan{})
			if diagnostic != nil {
				t.Fatal(diagnostic)
			}
			model.Extensions = append(model.Extensions, ir.ProviderExtensionIR{ID: ir.ExtensionIDFrom(identity), Provider: provider, Version: dataincarnation.Version, Owner: ir.ObjectID(model.Schema.ID), Kind: dataincarnation.Kind, Payload: "{}"})
		}
	}
	if contractEnabled {
		contract.DataIncarnation = &ir.DataIncarnationContractIR{Kind: "provider", Version: dataincarnation.Version}
	}
	modelBytes, err := ir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := ir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, err := ir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := ir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	modelDocument := golem.GeneratedSchemaDocument(uint32(ir.ModelFormatVersion), uint32(ir.CanonicalFormatVersion), digest(t, string(modelFingerprint)), modelBytes)
	contractDocument := golem.GeneratedSchemaDocument(uint32(ir.ContractFormatVersion), uint32(ir.CanonicalFormatVersion), digest(t, string(contractFingerprint)), contractBytes)
	providers := base.Providers()
	if physicalEnabled {
		for index, provider := range providers {
			snapshot, err := physical.CanonicalDecode(provider.Schema().Bytes())
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Version, snapshot.CanonicalVersion = physical.DataIncarnationFormatVersion, physical.DataIncarnationFormatVersion
			snapshot.System = physical.SystemSchema{Version: 1, Namespace: snapshot.Namespace, Objects: []physical.SystemObject{physical.DataIncarnationSystemObjectV1()}}
			providers[index] = providerDocument(t, provider.Provider(), snapshot)
		}
	}
	return golem.GeneratedSchemaBundle(base.GenerationDigest(), base.GeneratorVersion(), base.TemplateABIVersion(), modelDocument, contractDocument, providers...)
}

func TestDataIncarnationBundleAdmissionRequiresLogicalAndPhysicalAgreement(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		bundle := incarnationBundle(t, enabled, enabled, enabled)
		if _, err := DecodeActive(bundle); err != nil {
			t.Fatalf("valid bundle enabled=%v: %v", enabled, err)
		}
		if _, err := New(bundle); err != nil {
			t.Fatalf("valid registry enabled=%v: %v", enabled, err)
		}
		if _, err := NewHistorical(bundle); err != nil {
			t.Fatalf("supported retained profile enabled=%v: %v", enabled, err)
		}
		metadata := golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract())
		if _, err := DecodeActive(metadata); err != nil {
			t.Fatalf("metadata-only decode enabled=%v: %v", enabled, err)
		}
		if _, err := New(metadata); err == nil {
			t.Fatal("registry accepted absent provider inventory")
		}
	}
	for _, test := range []struct {
		name                      string
		model, contract, physical bool
	}{
		{"missing contract", true, false, true},
		{"missing model", false, true, true},
		{"missing physical", true, true, false},
		{"undeclared physical", false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle := incarnationBundle(t, test.model, test.contract, test.physical)
			if _, err := DecodeActive(bundle); err == nil || !strings.Contains(err.Error(), "data incarnation") {
				t.Fatalf("mixed bundle accepted by DecodeActive: %v", err)
			}
			if _, err := New(bundle); err == nil || !strings.Contains(err.Error(), "data incarnation") {
				t.Fatalf("mixed bundle accepted by New: %v", err)
			}
		})
	}
}

func TestDataIncarnationProviderOuterMetadataMustMatchDecodedProfile(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		bundle := incarnationBundle(t, enabled, enabled, enabled)
		providers := bundle.Providers()
		forgedVersion := physical.DataIncarnationFormatVersion
		if enabled {
			forgedVersion = physical.SchemaFormatVersion
		}
		original := providers[0]
		document := original.Schema()
		forged := golem.GeneratedSchemaDocument(forgedVersion, forgedVersion, document.Fingerprint(), document.Bytes())
		providers[0] = golem.GeneratedProviderSchemaDocument(original.Provider(), original.SystemFingerprint(), forged)
		bundle = golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), providers...)
		if _, err := DecodeActive(bundle); err == nil || !strings.Contains(err.Error(), "outer physical") {
			t.Fatalf("outer/inner mismatch accepted by DecodeActive enabled=%v: %v", enabled, err)
		}
		if _, err := New(bundle); err == nil || !strings.Contains(err.Error(), "outer physical") {
			t.Fatalf("outer/inner mismatch accepted by New enabled=%v: %v", enabled, err)
		}
		if _, err := NewHistorical(bundle); err == nil {
			t.Fatalf("outer/inner mismatch accepted by history enabled=%v", enabled)
		}
	}
}
