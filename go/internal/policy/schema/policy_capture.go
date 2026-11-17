package schema

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"slices"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

// PolicyCaptureFields returns the immutable, actor-independent producer image
// inventory for one physical model. It is derived once from every registered
// scope and fact template; caller readability and active subscriptions do not
// contribute to it. It does not certify historical evidence or enable capture.
func (registry *Registry) PolicyCaptureFields(model golem.ModelID) []golem.FieldID {
	if registry == nil {
		return nil
	}
	return append([]golem.FieldID(nil), registry.policyCapture.fields[model]...)
}

// PolicyCaptureRoots returns the sorted models that own static scope or fact
// templates. A physical model reached only by another owner's template is not
// a capture root, even though PolicyCaptureFields includes its evidence.
func (registry *Registry) PolicyCaptureRoots() []golem.ModelID {
	if registry == nil {
		return nil
	}
	return slices.Clone(registry.policyCapture.roots)
}

// PolicyCaptureProjections returns every declaration's factorized path
// inventory for a root, in canonical scope-template then fact-template order.
// Independent occurrences and duplicate paths across declarations are retained.
// All nested slices belong to the caller; this is static graph shape, not a
// snapshot certificate or actor-dependent materialized policy result.
func (registry *Registry) PolicyCaptureProjections(root golem.ModelID) []scopecontract.CaptureProjection {
	if registry == nil {
		return nil
	}
	return clonePolicyCaptureProjections(registry.policyCapture.projections[root])
}

// PolicyCaptureFingerprint identifies the static physical evidence inventory,
// including the logical model's scalar types and relation correlations. It is
// derived once at bootstrap and does not certify an actor or a snapshot.
func (registry *Registry) PolicyCaptureFingerprint() golem.SchemaDigest {
	if registry == nil {
		return golem.SchemaDigest{}
	}
	return registry.policyCapture.fingerprint
}

type policyCaptureInventory struct {
	fingerprint golem.SchemaDigest
	fields      map[golem.ModelID][]golem.FieldID
	roots       []golem.ModelID
	projections map[golem.ModelID][]scopecontract.CaptureProjection
}

func clonePolicyCaptureProjections(source []scopecontract.CaptureProjection) []scopecontract.CaptureProjection {
	result := slices.Clone(source)
	for index := range result {
		result[index].Fields = slices.Clone(source[index].Fields)
		result[index].Path = slices.Clone(source[index].Path)
		for hop := range result[index].Path {
			result[index].Path[hop].ParentFields = slices.Clone(source[index].Path[hop].ParentFields)
			result[index].Path[hop].ChildFields = slices.Clone(source[index].Path[hop].ChildFields)
		}
	}
	return result
}

// derivePolicyCaptureFields consumes only the already validated compilation.
// The result is a private execution inventory, never a second persisted schema
// footprint or a set of materialized policy truths.
func derivePolicyCaptureFields(compilation compilerir.CompilationIR, modelFingerprint golem.SchemaDigest) (policyCaptureInventory, error) {
	hasTemplates := false
	for _, owner := range compilation.Contract.Models {
		hasTemplates = hasTemplates || len(owner.ScopeTemplates) != 0 || len(owner.FactTemplates) != 0
	}
	if !hasTemplates {
		return policyCaptureInventory{}, nil
	}
	result := policyCaptureInventory{projections: make(map[golem.ModelID][]scopecontract.CaptureProjection)}
	fields := map[golem.ModelID]map[golem.FieldID]bool{}
	add := func(root golem.ModelID, projections []scopecontract.CaptureProjection) error {
		result.projections[root] = append(result.projections[root], clonePolicyCaptureProjections(projections)...)
		for _, projection := range projections {
			model, err := modelID(projection.ModelID)
			if err != nil {
				return err
			}
			if fields[model] == nil {
				fields[model] = map[golem.FieldID]bool{}
			}
			for _, id := range projection.Fields {
				field, err := fieldID(id)
				if err != nil {
					return err
				}
				fields[model][field] = true
			}
		}
		return nil
	}
	for _, owner := range compilation.Contract.Models {
		if len(owner.ScopeTemplates) == 0 && len(owner.FactTemplates) == 0 {
			continue
		}
		root, err := modelID(owner.ModelID)
		if err != nil {
			return policyCaptureInventory{}, err
		}
		result.roots = append(result.roots, root)
		for _, template := range owner.ScopeTemplates {
			projection, err := scopecontract.Projection(compilation.Model, template)
			if err != nil {
				return policyCaptureInventory{}, err
			}
			if err := add(root, projection); err != nil {
				return policyCaptureInventory{}, err
			}
		}
		for _, template := range owner.FactTemplates {
			projection, err := scopecontract.FactProjection(compilation.Model, template)
			if err != nil {
				return policyCaptureInventory{}, err
			}
			if err := add(root, projection); err != nil {
				return policyCaptureInventory{}, err
			}
		}
	}
	sort.Slice(result.roots, func(left, right int) bool { return bytes.Compare(result.roots[left][:], result.roots[right][:]) < 0 })
	result.fields = make(map[golem.ModelID][]golem.FieldID, len(fields))
	for model, set := range fields {
		values := make([]golem.FieldID, 0, len(set))
		for field := range set {
			values = append(values, field)
		}
		sort.Slice(values, func(left, right int) bool { return bytes.Compare(values[left][:], values[right][:]) < 0 })
		result.fields[model] = values
	}
	result.fingerprint = fingerprintPolicyCapture(modelFingerprint, result)
	return result, nil
}

// Fixed-width counts and length-prefixed strings make every boundary explicit.
// Root order and declaration/projection order were canonicalized at bootstrap;
// correlation pairs and independent occurrences are deliberately not sorted.
func fingerprintPolicyCapture(modelFingerprint golem.SchemaDigest, inventory policyCaptureInventory) golem.SchemaDigest {
	if len(inventory.roots) == 0 {
		return golem.SchemaDigest{}
	}
	writer := policyCaptureHashWriter{hash: sha256.New()}
	writer.text("golem:policy-capture-fingerprint:v1")
	_, _ = writer.hash.Write(modelFingerprint[:])
	writer.count(len(inventory.roots))
	for _, root := range inventory.roots {
		_, _ = writer.hash.Write(root[:])
		projections := inventory.projections[root]
		writer.count(len(projections))
		for _, projection := range projections {
			writer.text(string(projection.ModelID))
			writer.count(len(projection.Path))
			for _, hop := range projection.Path {
				writer.text(hop.Occurrence)
				writer.text(string(hop.ModelID))
				writer.text(string(hop.FieldID))
				writer.text(string(hop.RelationID))
				writer.text(string(hop.TargetModelID))
				writer.fields(hop.ParentFields)
				writer.fields(hop.ChildFields)
			}
			writer.fields(projection.Fields)
		}
	}
	var result golem.SchemaDigest
	copy(result[:], writer.hash.Sum(nil))
	return result
}

type policyCaptureHashWriter struct{ hash hash.Hash }

func (writer policyCaptureHashWriter) count(count int) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(count))
	_, _ = writer.hash.Write(encoded[:])
}
func (writer policyCaptureHashWriter) text(value string) {
	writer.count(len(value))
	_, _ = writer.hash.Write([]byte(value))
}
func (writer policyCaptureHashWriter) fields(fields []compilerir.FieldID) {
	writer.count(len(fields))
	for _, field := range fields {
		writer.text(string(field))
	}
}
