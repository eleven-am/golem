package ir

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
)

const (
	conditionDomain            = "golem:policy-condition:v1\x00"
	policyDomain               = "golem:policy:v1\x00"
	conditionFingerprintDomain = "golem:policy-condition-fingerprint:v1\x00"
	policyFingerprintDomain    = "golem:policy-fingerprint:v1\x00"
)

type Fingerprint [32]byte

func CanonicalCondition(condition Condition) ([]byte, error) {
	if err := condition.Validate(); err != nil {
		return nil, err
	}
	encoder := canonicalEncoder{}
	encoder.raw([]byte(conditionDomain))
	encoder.id16(condition.ModelID())
	encoder.condition(condition)
	if encoder.err != nil {
		return nil, encoder.err
	}
	return encoder.buffer.Bytes(), nil
}
func ConditionFingerprint(condition Condition) (Fingerprint, error) {
	encoded, err := CanonicalCondition(condition)
	if err != nil {
		return Fingerprint{}, err
	}
	return hashCanonical(conditionFingerprintDomain, encoded), nil
}
func CanonicalPolicy(policy Policy) ([]byte, error) {
	if policy.model == (ModelID{}) {
		return nil, fmt.Errorf("policy IR: zero policy model")
	}
	validated, err := NewPolicy(policy.model, policy.rules)
	if err != nil {
		return nil, err
	}
	encoder := canonicalEncoder{}
	encoder.raw([]byte(policyDomain))
	encoder.id16(validated.model)
	encoder.count(len(validated.rules))
	for _, rule := range validated.rules {
		encoder.rule(rule)
	}
	if encoder.err != nil {
		return nil, encoder.err
	}
	return encoder.buffer.Bytes(), nil
}
func hashCanonical(domain string, encoded []byte) Fingerprint {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write(encoded)
	var result Fingerprint
	copy(result[:], hash.Sum(nil))
	return result
}

type canonicalEncoder struct {
	buffer bytes.Buffer
	err    error
}

func (e *canonicalEncoder) raw(value []byte) {
	if e.err == nil {
		_, e.err = e.buffer.Write(value)
	}
}
func (e *canonicalEncoder) u8(value uint8) { e.raw([]byte{value}) }
func (e *canonicalEncoder) u16(value uint16) {
	var data [2]byte
	binary.BigEndian.PutUint16(data[:], value)
	e.raw(data[:])
}
func (e *canonicalEncoder) u32(value uint32) {
	var data [4]byte
	binary.BigEndian.PutUint32(data[:], value)
	e.raw(data[:])
}
func (e *canonicalEncoder) u64(value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	e.raw(data[:])
}
func (e *canonicalEncoder) i64(value int64) { e.u64(uint64(value)) }
func (e *canonicalEncoder) count(length int) {
	if length < 0 || uint64(length) > math.MaxUint32 {
		e.fail("collection length exceeds uint32")
		return
	}
	e.u32(uint32(length))
}
func (e *canonicalEncoder) bytes(value []byte)  { e.count(len(value)); e.raw(value) }
func (e *canonicalEncoder) text(value string)   { e.bytes([]byte(value)) }
func (e *canonicalEncoder) id16(value [16]byte) { e.raw(value[:]) }
func (e *canonicalEncoder) fail(message string) {
	if e.err == nil {
		e.err = fmt.Errorf("policy IR canonical encoding: %s", message)
	}
}

func (e *canonicalEncoder) typeRef(value TypeRef) {
	e.u8(uint8(value.kind))
	e.boolean(value.nullable)
	e.u16(value.precision)
	e.u16(value.scale)
	e.id16(value.enum)
	e.u16(uint16(value.capability))
	if value.element == nil {
		e.u8(0)
	} else {
		e.u8(1)
		e.typeRef(*value.element)
	}
}
func (e *canonicalEncoder) boolean(value bool) {
	if value {
		e.u8(1)
	} else {
		e.u8(0)
	}
}
func (e *canonicalEncoder) value(value Value) {
	e.u8(uint8(value.kind))
	switch value.kind {
	case ValueBool:
		e.boolean(value.boolean)
	case ValueInt16, ValueInt32, ValueInt64:
		e.i64(value.signed)
	case ValueFloat32:
		e.u32(value.float32Bits)
	case ValueFloat64:
		e.u64(value.float64Bits)
	case ValueDecimal:
		e.i64(value.decimal.coefficient)
		e.u8(value.decimal.scale)
	case ValueString:
		e.text(value.text)
	case ValueBytes:
		e.bytes(value.bytes)
	case ValueUUID:
		e.raw(value.uuid[:])
	case ValueDate:
		e.u16(uint16(value.date.year))
		e.u8(value.date.month)
		e.u8(value.date.day)
	case ValueTime:
		e.i64(value.time.microseconds)
	case ValueDateTime:
		e.i64(value.instant.unixSeconds)
		e.u32(value.instant.nanosecond)
	case ValueEnum:
		e.id16(value.enum.enum)
		e.id16(value.enum.value)
	case ValueJSON:
		e.json(value.json)
	case ValueScalarList:
		e.count(len(value.list))
		for _, element := range value.list {
			e.value(element)
		}
	default:
		e.fail("unknown value kind")
	}
}
func (e *canonicalEncoder) jsonNumber(value JSONNumberValue) {
	e.boolean(value.negative)
	e.bytes(value.coefficient)
	e.u32(uint32(value.exponent))
}
func (e *canonicalEncoder) json(value JSONValue) {
	e.u8(uint8(value.kind))
	switch value.kind {
	case JSONNull:
	case JSONBool:
		e.boolean(value.boolean)
	case JSONNumber:
		e.jsonNumber(value.number)
	case JSONString:
		e.text(value.text)
	case JSONArray:
		e.count(len(value.array))
		for _, item := range value.array {
			e.json(item)
		}
	case JSONObject:
		e.count(len(value.object))
		for _, member := range value.object {
			e.text(member.key)
			e.json(member.value)
		}
	default:
		e.fail("unknown JSON kind")
	}
}
func (e *canonicalEncoder) operand(value Operand) {
	e.u8(uint8(value.kind))
	switch value.kind {
	case OperandNone:
	case OperandOne:
		e.value(value.one)
	case OperandMany:
		e.count(len(value.many))
		for _, item := range value.many {
			e.value(item)
		}
	case OperandFlag:
		e.boolean(value.flag)
	case OperandJSONNull:
		e.u8(uint8(value.jsonNull))
	default:
		e.fail("unknown operand kind")
	}
}
func (e *canonicalEncoder) requirements(values []Requirement) {
	e.count(len(values))
	for _, value := range values {
		e.u8(uint8(value.providers))
		e.u16(uint16(value.capability))
	}
}
func (e *canonicalEncoder) path(path JSONPath) {
	e.count(len(path.segments))
	for _, segment := range path.segments {
		e.boolean(segment.isIndex)
		if segment.isIndex {
			e.u64(segment.arrayIndex)
		} else {
			e.text(segment.key)
		}
	}
}
func (e *canonicalEncoder) condition(condition Condition) {
	e.u8(uint8(condition.Kind()))
	switch node := condition.node.(type) {
	case constantNode:
		e.id16(node.model)
		e.boolean(node.truth)
	case logicalNode:
		e.id16(node.model)
		e.u8(uint8(node.operator))
		e.count(len(node.children))
		for _, child := range node.children {
			e.condition(child)
		}
		e.requirements(node.requirements)
	case scalarNode:
		e.id16(node.model)
		e.id16(node.field)
		e.typeRef(node.fieldType)
		e.u16(uint16(node.operator))
		e.u8(uint8(node.mode))
		e.operand(node.operand)
		e.requirements(node.requirements)
	case listNode:
		e.id16(node.model)
		e.id16(node.field)
		e.typeRef(node.fieldType)
		e.u16(uint16(node.operator))
		e.operand(node.operand)
		e.requirements(node.requirements)
	case jsonNode:
		e.id16(node.model)
		e.id16(node.field)
		e.typeRef(node.fieldType)
		e.u16(uint16(node.operator))
		e.u8(uint8(node.mode))
		e.path(node.path)
		e.operand(node.operand)
		e.requirements(node.requirements)
	case relationNode:
		e.id16(node.model)
		e.id16(node.field)
		e.id16(node.relation)
		e.id16(node.target)
		e.u8(uint8(node.cardinality))
		e.u16(uint16(node.operator))
		if node.child == nil {
			e.u8(0)
		} else {
			e.u8(1)
			e.condition(*node.child)
		}
		e.requirements(node.requirements)
	default:
		e.fail("unknown condition variant")
	}
}
func (e *canonicalEncoder) rule(rule Rule) {
	e.u8(uint8(rule.action))
	e.u8(uint8(rule.effect))
	e.id16(rule.model)
	if rule.condition == nil {
		e.u8(0)
	} else {
		e.u8(1)
		e.condition(*rule.condition)
	}
	if rule.fields == nil {
		e.u8(0)
	} else {
		e.u8(1)
		e.count(len(rule.fields))
		for _, field := range rule.fields {
			e.id16(field)
		}
	}
	e.u32(rule.position)
}
