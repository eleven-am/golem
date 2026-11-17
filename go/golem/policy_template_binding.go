package golem

import (
	"bytes"
	"encoding/hex"
	"go/token"
	"slices"
	"strings"
)

const frozenTemplatePolicyVersion uint16 = 4
const maxTemplateBindingBytes = 1 << 20

const scopeBindingDomain = "golem:public-scope-binding:v1\x00"
const factBindingDomain = "golem:public-fact-binding:v1\x00"

// BoundFactScope is a generated, parameterized policy scope. It deliberately
// has no caller predicate or selection methods. Binding validates its actual
// expression and arguments against the active registry's template.
type BoundFactScope[M any] struct {
	predicate Predicate[M]
	binding   *frozenTemplateBinding
}

// TemplateArgument is an immutable generated scalar or set argument.
type TemplateArgument struct {
	name    string
	operand frozenOperand
}

func GeneratedTemplateArgument[V any](name string, value V) TemplateArgument {
	return TemplateArgument{name: name, operand: frozenOperand{kind: FrozenOperandOne, one: scalarValueAny(value)}}
}

func GeneratedTemplateSetArgument[V any](name string, values []V) TemplateArgument {
	items := make([]frozenValue, len(values))
	for index, value := range values {
		items[index] = scalarValueAny(value)
	}
	return TemplateArgument{name: name, operand: frozenOperand{kind: FrozenOperandMany, many: items}}
}

// GeneratedBoundFactScope is the generated-code seam. Metadata attached to an
// independently supplied predicate is not trusted by the runtime binder.
func GeneratedBoundFactScope[M any](model ModelID, templateID string, fingerprint SchemaDigest, predicate Predicate[M], arguments ...TemplateArgument) BoundFactScope[M] {
	return BoundFactScope[M]{predicate: predicate, binding: generatedTemplateBinding(model, templateID, fingerprint, arguments)}
}

func generatedTemplateBinding(model ModelID, templateID string, fingerprint SchemaDigest, arguments []TemplateArgument) *frozenTemplateBinding {
	owned := make([]TemplateArgument, len(arguments))
	for index, argument := range arguments {
		owned[index] = TemplateArgument{name: argument.name, operand: cloneFrozenOperand(argument.operand)}
	}
	return &frozenTemplateBinding{model: model, template: templateID, fingerprint: fingerprint, arguments: owned}
}

// BindFactScope binds the policy's one generated scope. Ordinary inline
// scopes continue to use FactScope; both paths share the same declaration gate.
func (rules *Rules[M]) BindFactScope(scope BoundFactScope[M]) {
	rules.state.mu.Lock()
	defer rules.state.mu.Unlock()
	rules.state.scope = scope.predicate.node
	rules.state.scopeBinding = scope.binding
	rules.state.scopeDeclarations++
}

type frozenTemplateBinding struct {
	model       ModelID
	template    string
	fingerprint SchemaDigest
	arguments   []TemplateArgument
	canonical   []byte
}

func freezeTemplateBinding(binding *frozenTemplateBinding, model ModelID, domain string) (*frozenTemplateBinding, error) {
	if binding == nil {
		return nil, nil
	}
	id, err := hex.DecodeString(binding.template)
	if err != nil || len(id) != 16 || bytes.Equal(id, make([]byte, 16)) || hex.EncodeToString(id) != binding.template || binding.model != model || binding.fingerprint == (SchemaDigest{}) || len(binding.arguments) > 64 {
		return nil, freezeFailure(FreezeInvalidRule, "invalid generated template identity, model, fingerprint or argument inventory")
	}
	result := &frozenTemplateBinding{model: model, template: binding.template, fingerprint: binding.fingerprint, arguments: make([]TemplateArgument, len(binding.arguments))}
	for index, argument := range binding.arguments {
		if len(argument.name) > 256 || !token.IsIdentifier(argument.name) || !token.IsExported(argument.name) || argument.operand.kind != FrozenOperandOne && argument.operand.kind != FrozenOperandMany {
			return nil, freezeFailure(FreezeInvalidRule, "template arguments must be named exported scalar or set parameters")
		}
		if err := validateFrozenOperand(argument.operand); err != nil {
			return nil, err
		}
		result.arguments[index] = TemplateArgument{name: argument.name, operand: cloneFrozenOperand(argument.operand)}
	}
	slices.SortFunc(result.arguments, func(left, right TemplateArgument) int { return strings.Compare(left.name, right.name) })
	var encoded bytes.Buffer
	encoded.WriteString(domain)
	encoded.Write(model[:])
	encoded.Write(id)
	encoded.Write(binding.fingerprint[:])
	writeUint32(&encoded, uint32(len(result.arguments)))
	for index, argument := range result.arguments {
		if index != 0 && result.arguments[index-1].name == argument.name {
			return nil, freezeFailure(FreezeInvalidRule, "template argument is repeated")
		}
		writeBytes(&encoded, []byte(argument.name))
		if err := encodeFrozenOperand(&encoded, argument.operand); err != nil {
			return nil, err
		}
		if encoded.Len() > maxTemplateBindingBytes {
			return nil, freezeFailure(FreezeInvalidValue, "template arguments exceed one MiB")
		}
	}
	result.canonical = encoded.Bytes()
	return result, nil
}

// FrozenTemplateBindingView retains the exact generated template inputs even when
// Boolean normalization has folded an empty set or a constant branch.
type FrozenTemplateBindingView interface {
	sealedFrozenTemplateBindingView()
	TemplateID() string
	Fingerprint() SchemaDigest
	Arguments() []FrozenTemplateArgumentView
	CanonicalBytes() []byte
}

type FrozenTemplateArgumentView interface {
	sealedFrozenTemplateArgumentView()
	Name() string
	Operand() FrozenOperandView
}

type frozenTemplateBindingView struct{ binding *frozenTemplateBinding }
type frozenTemplateArgumentView struct{ argument TemplateArgument }

func (frozenTemplateBindingView) sealedFrozenTemplateBindingView() {}
func (view frozenTemplateBindingView) TemplateID() string          { return view.binding.template }
func (view frozenTemplateBindingView) Fingerprint() SchemaDigest   { return view.binding.fingerprint }
func (view frozenTemplateBindingView) CanonicalBytes() []byte {
	return append([]byte(nil), view.binding.canonical...)
}
func (view frozenTemplateBindingView) Arguments() []FrozenTemplateArgumentView {
	result := make([]FrozenTemplateArgumentView, len(view.binding.arguments))
	for index, argument := range view.binding.arguments {
		result[index] = frozenTemplateArgumentView{argument: TemplateArgument{name: argument.name, operand: cloneFrozenOperand(argument.operand)}}
	}
	return result
}
func (frozenTemplateArgumentView) sealedFrozenTemplateArgumentView() {}
func (view frozenTemplateArgumentView) Name() string                 { return view.argument.name }
func (view frozenTemplateArgumentView) Operand() FrozenOperandView {
	return frozenOperandView{operand: cloneFrozenOperand(view.argument.operand)}
}
func (view frozenPolicyView) FactScopeBinding() (FrozenTemplateBindingView, bool) {
	if view.policy.scopeBinding == nil {
		return nil, false
	}
	return frozenTemplateBindingView{binding: view.policy.scopeBinding}, true
}
