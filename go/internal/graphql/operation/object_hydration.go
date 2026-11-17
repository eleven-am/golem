package operation

import (
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
)

// ObjectHydration is execution-local. It retains only identities from supplied
// rows, grouped by caller projection, before any response encoding can run.
type ObjectHydration struct {
	compiler    *Compiler
	typ         compilerir.GraphQLTypeIR
	slots       []selectset.Slot
	groups      []objectHydrationGroup
	occurrences int
	operation   string
}

type objectHydrationGroup struct {
	spec      selectset.ModelSelection
	hydration *Hydration
}

type objectRowReference struct {
	group    int
	identity golem.RuntimeSemanticIdentityToken
}

// hydratedObject keys values by response occurrence, so two aliases of the
// same DTO member cannot share or overwrite differently projected model rows.
type hydratedObject struct {
	fields map[string]any
	gate   *objectRowReference
}
type omittedObject struct{}

func (c *Compiler) CustomObjectHydration(root CustomRoot) (*ObjectHydration, bool) {
	operation := "customQuery"
	if root.Kind == compilerir.CustomOperationMutation {
		operation = "customMutation"
	}
	return c.objectHydration(root.Result, root.Slots, operation)
}

func (c *Compiler) ComputedObjectHydration(slot selectset.Slot) (*ObjectHydration, bool) {
	if slot.Computed == nil {
		return nil, false
	}
	return c.objectHydration(slot.Computed.Result, slot.Children, "computed")
}

func (c *Compiler) objectHydration(typ compilerir.GraphQLTypeIR, slots []selectset.Slot, operation string) (*ObjectHydration, bool) {
	if !HoldsObject(typ) || !objectSlotsHoldRows(slots) {
		return nil, false
	}
	return &ObjectHydration{compiler: c, typ: typ, slots: slots, operation: operation}, true
}

func objectSlotsHoldRows(slots []selectset.Slot) bool {
	for _, slot := range slots {
		if slot.Model != nil || objectSlotsHoldRows(slot.Children) {
			return true
		}
	}
	return false
}

func (h *ObjectHydration) Groups() []*Hydration {
	result := make([]*Hydration, len(h.groups))
	for i := range h.groups {
		result[i] = h.groups[i].hydration
	}
	return result
}

func (h *ObjectHydration) Collect(value any) (any, error) {
	return h.prepare(h.typ, value, h.slots, nil)
}

func (h *ObjectHydration) prepare(typ compilerir.GraphQLTypeIR, value any, slots []selectset.Slot, model *selectset.ModelSelection) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch typ.Kind {
	case compilerir.GraphQLTypeList:
		items, ok := value.([]any)
		if !ok || typ.Element == nil {
			return nil, fmt.Errorf("DTO hydration list has value %T", value)
		}
		result := make([]any, len(items))
		for i, item := range items {
			prepared, err := h.prepare(*typ.Element, item, slots, model)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			result[i] = prepared
		}
		return result, nil
	case compilerir.GraphQLTypeObject:
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("DTO hydration object has value %T", value)
		}
		var definition *compilerir.GraphQLTypeDefinitionIR
		for i := range h.compiler.compilation.Contract.GraphQLTypes {
			candidate := &h.compiler.compilation.Contract.GraphQLTypes[i]
			if candidate.Name == typ.Name && candidate.Kind == typ.Kind {
				definition = candidate
				break
			}
		}
		if definition == nil {
			return nil, fmt.Errorf("DTO hydration definition %s is absent", typ.Name)
		}
		fields := make(map[string]compilerir.GraphQLTypeIR, len(definition.Fields))
		for _, field := range definition.Fields {
			fields[field.Name] = field.Type
		}
		result := hydratedObject{fields: make(map[string]any, len(slots))}
		prepareSlot := func(slot selectset.Slot) (any, error) {
			field, exists := fields[slot.ObjectField]
			if !exists {
				return nil, fmt.Errorf("DTO hydration field %s.%s is absent", typ.Name, slot.ObjectField)
			}
			item, present := object[slot.ObjectField]
			if !present {
				return nil, fmt.Errorf("DTO result field %s.%s is absent", typ.Name, slot.ObjectField)
			}
			prepared, err := h.prepare(field, item, slot.Children, slot.Model)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", typ.Name, slot.ObjectField, err)
			}
			return prepared, nil
		}
		gateIndex := -1
		var gateValue any
		if definition.RowGate != "" {
			for i, slot := range slots {
				if slot.ObjectField == definition.RowGate {
					gateIndex = i
					break
				}
			}
			if gateIndex < 0 {
				return nil, fmt.Errorf("DTO row gate was not compiled")
			}
			var err error
			gateValue, err = prepareSlot(slots[gateIndex])
			if err != nil {
				return nil, err
			}
			gate := objectRowReference{group: -1}
			if gateValue != nil {
				var ok bool
				gate, ok = gateValue.(objectRowReference)
				if !ok {
					return nil, fmt.Errorf("DTO gate is not a model row")
				}
			}
			result.gate = &gate
		}
		for i, slot := range slots {
			if slot.Kind == selectset.SlotTypename || slot.Kind == selectset.SlotObjectGate {
				continue
			}
			prepared := gateValue
			if i != gateIndex {
				var err error
				prepared, err = prepareSlot(slot)
				if err != nil {
					return nil, err
				}
			}
			result.fields[slot.ResponseName] = prepared
		}
		return result, nil
	case compilerir.GraphQLTypeModel:
		if model == nil {
			return nil, fmt.Errorf("DTO model occurrence has no caller projection")
		}
		row, ok := value.(golem.RuntimeModelRow)
		if !ok {
			return nil, fmt.Errorf("DTO model occurrence has value %T", value)
		}
		h.occurrences++
		if h.occurrences > h.compiler.limits.ListItems {
			return nil, golem.RuntimeLimitError(h.operation, golem.ModelID{}, golem.LimitListItems, h.compiler.limits.ListItems, "DTO result exceeds the total model hydration limit", nil)
		}
		group := -1
		for i := range h.groups {
			if h.groups[i].spec.Model == model.Model && reflect.DeepEqual(h.groups[i].spec.Projection, model.Projection) {
				group = i
				break
			}
		}
		if group < 0 {
			leaf := typ
			leaf.Nullable = true
			hydration, err := h.compiler.newHydration(leaf, model.Model, model.Projection, nil, false)
			if err != nil {
				return nil, err
			}
			group = len(h.groups)
			h.groups = append(h.groups, objectHydrationGroup{spec: *model, hydration: hydration})
		}
		hydration := h.groups[group].hydration
		if err := hydration.Collect(row); err != nil {
			return nil, err
		}
		identity, err := hydration.identityOf(row)
		if err != nil {
			return nil, err
		}
		return objectRowReference{group: group, identity: identity.Token()}, nil
	default:
		return value, nil
	}
}

func (h *ObjectHydration) Restore(value any, rows []HydratedRows) (any, int, error) {
	if len(rows) != len(h.groups) {
		return nil, 0, fmt.Errorf("DTO hydration groups are incomplete")
	}
	restored, absent, err := restoreObjectValue(value, rows)
	if _, omitted := restored.(omittedObject); omitted {
		restored = nil
	}
	return restored, absent, err
}

func restoreObjectValue(value any, rows []HydratedRows) (any, int, error) {
	switch item := value.(type) {
	case objectRowReference:
		if item.group < 0 || item.group >= len(rows) {
			return nil, 0, fmt.Errorf("DTO hydration group is invalid")
		}
		row, present := rows[item.group][item.identity]
		if !present {
			return nil, 1, nil
		}
		return row, 0, nil
	case hydratedObject:
		if gate := item.gate; gate != nil {
			if gate.group < 0 {
				return omittedObject{}, 1, nil
			}
			if gate.group >= len(rows) {
				return nil, 0, fmt.Errorf("DTO row gate group is invalid")
			}
			if _, readable := rows[gate.group][gate.identity]; !readable {
				return omittedObject{}, 1, nil
			}
		}
		result := hydratedObject{fields: make(map[string]any, len(item.fields))}
		absent := 0
		for name, field := range item.fields {
			restored, missing, err := restoreObjectValue(field, rows)
			if err != nil {
				return nil, 0, err
			}
			if _, omitted := restored.(omittedObject); omitted {
				restored = nil
			}
			result.fields[name] = restored
			absent += missing
		}
		return result, absent, nil
	case []any:
		result := make([]any, 0, len(item))
		absent := 0
		for _, field := range item {
			restored, missing, err := restoreObjectValue(field, rows)
			if err != nil {
				return nil, 0, err
			}
			if _, omitted := restored.(omittedObject); !omitted {
				result = append(result, restored)
			}
			absent += missing
		}
		return result, absent, nil
	default:
		return value, 0, nil
	}
}
