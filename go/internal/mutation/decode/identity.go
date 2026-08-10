package decode

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type IdentityComponent struct {
	field policyir.FieldID
	value policyir.Value
	null  bool
}

func IdentityValue(field policyir.FieldID, value policyir.Value) (IdentityComponent, error) {
	if field == (policyir.FieldID{}) || value.Kind() == 0 {
		return IdentityComponent{}, fmt.Errorf("P4_MUTATION_IDENTITY: component field and value are required")
	}
	if err := value.Validate(); err != nil {
		return IdentityComponent{}, fmt.Errorf("P4_MUTATION_IDENTITY: invalid component value: %w", err)
	}
	return IdentityComponent{field: field, value: value}, nil
}

func IdentityNull(field policyir.FieldID) (IdentityComponent, error) {
	if field == (policyir.FieldID{}) {
		return IdentityComponent{}, fmt.Errorf("P4_MUTATION_IDENTITY: component field is zero")
	}
	return IdentityComponent{field: field, null: true}, nil
}

func (component IdentityComponent) FieldID() policyir.FieldID { return component.field }
func (component IdentityComponent) IsNull() bool              { return component.null }
func (component IdentityComponent) PolicyValue() (policyir.Value, bool) {
	return component.value, !component.null
}

// Identity preserves key-declared component order. It supports scalar and
// composite primary/unique identities without reducing values to strings.
type Identity struct {
	key        golem.KeyID
	components []IdentityComponent
}

func NewIdentity(key golem.KeyID, components []IdentityComponent) (Identity, error) {
	if key == (golem.KeyID{}) || len(components) == 0 {
		return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: key and components are required")
	}
	seen := make(map[policyir.FieldID]struct{}, len(components))
	result := Identity{key: key, components: append([]IdentityComponent(nil), components...)}
	for _, component := range result.components {
		if component.field == (policyir.FieldID{}) {
			return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: component field is zero")
		}
		if _, duplicate := seen[component.field]; duplicate {
			return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: component field appears more than once")
		}
		seen[component.field] = struct{}{}
		if component.null {
			if component.value.Kind() != 0 {
				return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: NULL component carries a value")
			}
		} else if err := component.value.Validate(); err != nil {
			return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: invalid component value: %w", err)
		}
	}
	return result, nil
}

func (identity Identity) KeyID() golem.KeyID { return identity.key }
func (identity Identity) Components() []IdentityComponent {
	return append([]IdentityComponent(nil), identity.components...)
}

func ExtractIdentity(registry *schema.Registry, row Row, key golem.KeyID) (Identity, error) {
	if registry == nil {
		return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: active schema registry is required")
	}
	model, ok := registry.Model(golem.ModelID(row.model))
	if !ok {
		return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: row model is absent")
	}
	declared, ok := model.Identity(golem.KeyID(key))
	if key == (golem.KeyID{}) || !ok {
		return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: key is zero, foreign, or absent")
	}
	fields := declared.Fields()
	components := make([]IdentityComponent, len(fields))
	for index, publicField := range fields {
		field := policyir.FieldID(publicField)
		cell, found := row.Cell(field)
		if !found {
			return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: component field %x is absent", field)
		}
		components[index] = IdentityComponent{field: field, value: cell.value, null: cell.null}
	}
	return NewIdentity(key, components)
}

// ExtractOrderedIdentity resolves the exact key whose declared component order
// matches a FactRequirement identity inventory, then extracts only those
// components from a possibly partial row.
func ExtractOrderedIdentity(registry *schema.Registry, row Row, fields []policyir.FieldID) (Identity, error) {
	if registry == nil || len(fields) == 0 {
		return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: active registry and ordered identity fields are required")
	}
	if err := row.RequireFields(fields); err != nil {
		return Identity{}, err
	}
	model, ok := registry.Model(golem.ModelID(row.model))
	if !ok {
		return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: row model is absent")
	}
	for _, identity := range model.Identities() {
		declared := identity.Fields()
		if len(declared) != len(fields) {
			continue
		}
		matches := true
		for index := range declared {
			if policyir.FieldID(declared[index]) != fields[index] {
				matches = false
				break
			}
		}
		if matches {
			return ExtractIdentity(registry, row, identity.KeyID())
		}
	}
	return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: ordered fields do not name a declared identity")
}

// ValidateIdentity proves a decoded durable identity against the active model
// and logical field types without requiring a complete row image.
func ValidateIdentity(registry *schema.Registry, model policyir.ModelID, identity Identity) error {
	cells := make([]Cell, len(identity.components))
	fields := make([]policyir.FieldID, len(identity.components))
	for index, component := range identity.components {
		fields[index] = component.field
		if component.null {
			cells[index] = Null(component.field)
		} else {
			cells[index] = Value(component.field, component.value)
		}
	}
	row, err := NewRow(registry, model, cells)
	if err != nil {
		return err
	}
	validated, err := ExtractOrderedIdentity(registry, row, fields)
	if err != nil {
		return err
	}
	if validated.key != identity.key {
		return fmt.Errorf("P4_MUTATION_IDENTITY: decoded key does not match declared ordered fields")
	}
	return nil
}

func PrimaryIdentity(registry *schema.Registry, row Row) (Identity, error) {
	if registry == nil {
		return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: active schema registry is required")
	}
	model, ok := registry.Model(golem.ModelID(row.model))
	if !ok {
		return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: row model is absent")
	}
	for _, identity := range model.Identities() {
		if identity.Kind() == compilerir.KeyPrimary {
			return ExtractIdentity(registry, row, identity.KeyID())
		}
	}
	return Identity{}, fmt.Errorf("P4_MUTATION_IDENTITY: model has no primary identity")
}

type IdentityTransition struct {
	before *Identity
	after  *Identity
}

func (transition IdentityTransition) Before() (Identity, bool) {
	if transition.before == nil {
		return Identity{}, false
	}
	return *transition.before, true
}
func (transition IdentityTransition) After() (Identity, bool) {
	if transition.after == nil {
		return Identity{}, false
	}
	return *transition.after, true
}

// PrimaryIdentityTransition retains both sides of an identity-changing update.
// Exactly one side may be nil for create/delete.
func PrimaryIdentityTransition(registry *schema.Registry, before, after *Row) (IdentityTransition, error) {
	if before == nil && after == nil {
		return IdentityTransition{}, fmt.Errorf("P4_MUTATION_IDENTITY: before and after are both absent")
	}
	if before != nil && after != nil && before.model != after.model {
		return IdentityTransition{}, fmt.Errorf("P4_MUTATION_IDENTITY: before and after models differ")
	}
	result := IdentityTransition{}
	if before != nil {
		identity, err := PrimaryIdentity(registry, *before)
		if err != nil {
			return IdentityTransition{}, err
		}
		result.before = &identity
	}
	if after != nil {
		identity, err := PrimaryIdentity(registry, *after)
		if err != nil {
			return IdentityTransition{}, err
		}
		result.after = &identity
	}
	return result, nil
}
