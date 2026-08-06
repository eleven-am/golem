package operation

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

func (c *Compiler) EncodeRead(root ReadRoot, rows []golem.RuntimeModelRow) (any, error) {
	if c == nil {
		return nil, fmt.Errorf("P5_ENCODE_COMPILER: compiler is absent")
	}
	if root.Operation == 0 || root.Model == "" {
		return nil, fmt.Errorf("P5_ENCODE_ROOT: root metadata is absent")
	}
	encoded := make([]any, len(rows))
	for index, row := range rows {
		value, err := c.encodeRow(root.Model, row, root.Slots)
		if err != nil {
			return nil, fmt.Errorf("P5_ENCODE_ROW: %d: %w", index, err)
		}
		encoded[index] = value
	}
	if root.Operation == readir.FindUnique {
		if len(encoded) == 0 {
			return nil, nil
		}
		if len(encoded) != 1 {
			return nil, fmt.Errorf("P5_ENCODE_CARDINALITY: find-one returned %d rows", len(encoded))
		}
		return encoded[0], nil
	}
	return encoded, nil
}

func (c *Compiler) encodeRow(modelID compilerir.ModelID, row golem.RuntimeModelRow, slots []selectset.Slot) (map[string]any, error) {
	model, contract, ok := c.model(modelID)
	if !ok {
		return nil, fmt.Errorf("model %s is absent or unexposed", modelID)
	}
	publicModel, err := publicModelID(modelID)
	if err != nil || row.ModelID() != publicModel {
		return nil, fmt.Errorf("runtime row model does not match %s", modelID)
	}
	fields := make(map[compilerir.FieldID]compilerir.FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		fields[field.ID] = field
	}
	result := make(map[string]any, len(slots))
	for _, slot := range slots {
		switch slot.Kind {
		case selectset.SlotTypename:
			result[slot.ResponseName] = contract.GraphQLName
		case selectset.SlotScalar:
			field, present := fields[slot.FieldID]
			if !present || field.Scalar == nil || field.Kind == compilerir.FieldRelation {
				return nil, fmt.Errorf("scalar slot %s has no scalar field", slot.ResponseName)
			}
			fieldID, idErr := publicFieldID(slot.FieldID)
			if idErr != nil {
				return nil, idErr
			}
			cell := golem.RuntimeTransportField(row, fieldID)
			value, valueErr := c.encodeCell(cell, field.Scalar.Type)
			if valueErr != nil {
				return nil, fmt.Errorf("field %s: %w", slot.ResponseName, valueErr)
			}
			result[slot.ResponseName] = value
		case selectset.SlotRelation:
			field, present := fields[slot.FieldID]
			if !present || field.Relation == nil {
				return nil, fmt.Errorf("relation slot %s has no relation field", slot.ResponseName)
			}
			fieldID, idErr := publicFieldID(slot.FieldID)
			if idErr != nil {
				return nil, idErr
			}
			cell := golem.RuntimeTransportOccurrence(row, fieldID, golem.RuntimeOccurrenceID(slot.Occurrence))
			if cell.State() == golem.ReadUnselected {
				return nil, fmt.Errorf("selected relation %s is absent from the P3 row", slot.ResponseName)
			}
			if cell.State() == golem.ReadNull {
				result[slot.ResponseName] = nil
				continue
			}
			raw, _ := cell.Get()
			target, targetErr := c.relationTarget(modelID, field)
			if targetErr != nil {
				return nil, targetErr
			}
			switch value := raw.(type) {
			case golem.RuntimeModelRow:
				result[slot.ResponseName], err = c.encodeRow(target, value, slot.Children)
			case []golem.RuntimeModelRow:
				items := make([]any, len(value))
				for index, child := range value {
					items[index], err = c.encodeRow(target, child, slot.Children)
					if err != nil {
						break
					}
				}
				result[slot.ResponseName] = items
			default:
				err = fmt.Errorf("relation %s has runtime value %T", slot.ResponseName, raw)
			}
			if err != nil {
				return nil, err
			}
		case selectset.SlotRelationCount:
			counts := make(map[string]any, len(slot.Children))
			for _, child := range slot.Children {
				fieldID, idErr := publicFieldID(child.FieldID)
				if idErr != nil {
					return nil, idErr
				}
				relationID, idErr := publicRelationID(child.RelationID)
				if idErr != nil {
					return nil, idErr
				}
				cell := golem.RuntimeTransportRelationCount(row, fieldID, relationID, golem.RuntimeOccurrenceID(child.Occurrence))
				if cell.State() == golem.ReadUnselected {
					return nil, fmt.Errorf("selected relation count %s is absent from the P3 row", child.ResponseName)
				}
				if cell.State() == golem.ReadNull {
					counts[child.ResponseName] = nil
					continue
				}
				value, _ := cell.Get()
				count, ok := value.(int64)
				if !ok || count < 0 || count > int64(^uint32(0)>>1) {
					return nil, fmt.Errorf("relation count %s is outside GraphQL Int", child.ResponseName)
				}
				counts[child.ResponseName] = int32(count)
			}
			result[slot.ResponseName] = counts
		default:
			return nil, fmt.Errorf("slot %s has invalid kind %d", slot.ResponseName, slot.Kind)
		}
	}
	return result, nil
}

func (c *Compiler) encodeCell(cell golem.RuntimeTransportValue, logical compilerir.LogicalTypeIR) (any, error) {
	if cell.State() == golem.ReadUnselected {
		return nil, fmt.Errorf("selected scalar is absent from the P3 row")
	}
	if cell.State() == golem.ReadNull {
		return nil, nil
	}
	value, _ := cell.Get()
	return c.encodeLogical(logical, value)
}

func (c *Compiler) encodeLogical(logical compilerir.LogicalTypeIR, value any) (any, error) {
	switch logical.Kind {
	case compilerir.TypeBool:
		if result, ok := value.(bool); ok {
			return result, nil
		}
	case compilerir.TypeInt16:
		if result, ok := exactSigned(value, 16); ok {
			return int32(result), nil
		}
	case compilerir.TypeInt32:
		if result, ok := exactSigned(value, 32); ok {
			return int32(result), nil
		}
	case compilerir.TypeInt64:
		if result, ok := exactSigned(value, 64); ok {
			return graphqlscalar.SerializeBigInt(result), nil
		}
	case compilerir.TypeFloat32, compilerir.TypeFloat64:
		bits := 64
		if logical.Kind == compilerir.TypeFloat32 {
			bits = 32
		}
		if narrow, ok := value.(float32); ok {
			value = float64(narrow)
		}
		if result, err := graphqlscalar.Float(value, bits); err == nil {
			return result, nil
		}
	case compilerir.TypeDecimal:
		switch value := value.(type) {
		case golem.Decimal:
			return value.String(), nil
		case string:
			if parsed, err := graphqlscalar.Decimal(value); err == nil {
				return parsed.String(), nil
			}
		}
	case compilerir.TypeString:
		if result, ok := value.(string); ok {
			return result, nil
		}
	case compilerir.TypeBytes:
		switch value := value.(type) {
		case []byte:
			return base64.StdEncoding.EncodeToString(value), nil
		case string:
			if parsed, err := graphqlscalar.Bytes(value); err == nil {
				return base64.StdEncoding.EncodeToString(parsed), nil
			}
		}
	case compilerir.TypeUUID:
		switch value := value.(type) {
		case golem.UUID:
			return value.String(), nil
		case string:
			if parsed, err := graphqlscalar.UUID(value); err == nil {
				return parsed.String(), nil
			}
		}
	case compilerir.TypeDate:
		switch value := value.(type) {
		case golem.Date:
			return value.String(), nil
		case string:
			if parsed, err := graphqlscalar.Date(value); err == nil {
				return parsed.String(), nil
			}
		}
	case compilerir.TypeTime:
		switch value := value.(type) {
		case golem.Time:
			return value.String(), nil
		case string:
			if parsed, err := graphqlscalar.Time(value); err == nil {
				return parsed.String(), nil
			}
		}
	case compilerir.TypeDateTime:
		switch value := value.(type) {
		case time.Time:
			return graphqlscalar.SerializeDateTime(value), nil
		case string:
			if parsed, err := graphqlscalar.DateTime(value); err == nil {
				return graphqlscalar.SerializeDateTime(parsed), nil
			}
		}
	case compilerir.TypeJSON:
		switch value := value.(type) {
		case golem.JSONValue:
			encoded, err := golem.CanonicalJSON(value)
			if err != nil {
				return nil, err
			}
			return graphqlscalar.JSON(encoded, graphqlscalar.JSONLimits{})
		case json.RawMessage:
			return graphqlscalar.JSON(value, graphqlscalar.JSONLimits{})
		case interface{ Bytes() []byte }:
			return graphqlscalar.JSON(value.Bytes(), graphqlscalar.JSONLimits{})
		default:
			return value, nil
		}
	case compilerir.TypeEnum:
		wire, ok := value.(string)
		if ok && logical.EnumID != nil {
			if name, found := c.enumName(*logical.EnumID, wire); found {
				return name, nil
			}
		}
	case compilerir.TypeScalarList:
		list, ok := value.(golem.RuntimeScalarListValue)
		if !ok || logical.Element == nil {
			break
		}
		decoded, err := graphqlscalar.JSON(list.CanonicalJSON(), graphqlscalar.JSONLimits{})
		if err != nil {
			return nil, err
		}
		items, ok := decoded.([]any)
		if !ok {
			return nil, fmt.Errorf("scalar-list canonical value is not an array")
		}
		result := make([]any, len(items))
		for index, item := range items {
			result[index], err = c.encodeLogical(*logical.Element, item)
			if err != nil {
				return nil, fmt.Errorf("scalar-list item %d: %w", index, err)
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("logical type %s cannot serialize %T", logical.Kind, value)
}

func (c *Compiler) model(id compilerir.ModelID) (compilerir.ModelDeclIR, compilerir.ModelContractIR, bool) {
	var model compilerir.ModelDeclIR
	var contract compilerir.ModelContractIR
	modelOK, contractOK := false, false
	for _, value := range c.compilation.Model.Models {
		if value.ID == id {
			model, modelOK = value, true
			break
		}
	}
	for _, value := range c.compilation.Contract.Models {
		if value.ModelID == id && value.Exposed {
			contract, contractOK = value, true
			break
		}
	}
	return model, contract, modelOK && contractOK
}

func (c *Compiler) relationTarget(parent compilerir.ModelID, field compilerir.FieldIR) (compilerir.ModelID, error) {
	for _, relation := range c.compilation.Model.Relations {
		if field.Relation != nil && relation.ID == field.Relation.RelationID {
			if relation.SourceModel == parent && relation.SourceField == field.ID {
				return relation.TargetModel, nil
			}
			if relation.TargetModel == parent && relation.InverseField != nil && *relation.InverseField == field.ID {
				return relation.SourceModel, nil
			}
		}
	}
	return "", fmt.Errorf("relation target is absent for %s.%s", parent, field.ID)
}

func (c *Compiler) enumName(id compilerir.EnumID, wire string) (string, bool) {
	var valueID compilerir.EnumValueID
	found := false
	for _, enum := range c.compilation.Model.Enums {
		if enum.ID != id {
			continue
		}
		for _, value := range enum.Values {
			if value.WireValue == wire {
				valueID, found = value.ID, true
				break
			}
		}
	}
	if !found {
		return "", false
	}
	for _, enum := range c.compilation.Contract.Enums {
		if enum.EnumID == id {
			for _, value := range enum.Values {
				if value.ValueID == valueID {
					return value.GraphQLName, true
				}
			}
		}
	}
	return "", false
}

func exactSigned(value any, bits int) (int64, bool) {
	switch value := value.(type) {
	case int16:
		return int64(value), bits >= 16
	case int32:
		return int64(value), bits >= 32
	case int64:
		return value, true
	case int:
		return int64(value), true
	case json.Number:
		parsed, err := strconv.ParseInt(string(value), 10, bits)
		return parsed, err == nil
	}
	return 0, false
}

func publicModelID(value compilerir.ModelID) (golem.ModelID, error) {
	decoded, err := fixedPublicID(string(value))
	return golem.ModelID(decoded), err
}
func publicFieldID(value compilerir.FieldID) (golem.FieldID, error) {
	decoded, err := fixedPublicID(string(value))
	return golem.FieldID(decoded), err
}
func publicRelationID(value compilerir.RelationID) (golem.RelationID, error) {
	decoded, err := fixedPublicID(string(value))
	return golem.RelationID(decoded), err
}
func fixedPublicID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != value {
		return result, fmt.Errorf("identity %q is not canonical", value)
	}
	copy(result[:], decoded)
	return result, nil
}
