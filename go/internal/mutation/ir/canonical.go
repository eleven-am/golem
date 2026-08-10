package ir

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

const planDomain = "golem:mutation-plan:v2\x00"
const planFingerprintDomain = "golem:mutation-plan-fingerprint:v2\x00"

type Fingerprint [32]byte

func CanonicalPlan(plan Plan) ([]byte, error) {
	if err := plan.validate(); err != nil {
		return nil, err
	}
	encoder := canonicalEncoder{}
	encoder.raw([]byte(planDomain))
	encoder.u16(CanonicalFormatVersion)
	encoder.u8(uint8(plan.stance))
	encoder.u8(uint8(plan.retry))
	encoder.u32(plan.bounds.maxParameters)
	encoder.u32(plan.bounds.maxRows)
	encoder.count(len(plan.providers))
	for _, requirement := range plan.providers {
		encoder.u8(uint8(requirement.providers))
		encoder.u16(uint16(requirement.capability))
	}
	encoder.image(plan.result)
	if plan.factCodec == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.u16(plan.factCodec.formatVersion)
		encoder.text(plan.factCodec.codecID)
		encoder.raw(plan.factCodec.generation[:])
	}
	encoder.count(len(plan.graph.nodes))
	for _, node := range plan.graph.nodes {
		encoder.node(node)
	}
	if encoder.err != nil {
		return nil, encoder.err
	}
	return encoder.buffer.Bytes(), nil
}

func PlanFingerprint(plan Plan) (Fingerprint, error) {
	encoded, err := CanonicalPlan(plan)
	if err != nil {
		return Fingerprint{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(planFingerprintDomain))
	_, _ = hash.Write(encoded)
	var result Fingerprint
	copy(result[:], hash.Sum(nil))
	return result, nil
}

type canonicalEncoder struct {
	buffer bytes.Buffer
	err    error
}

func (encoder *canonicalEncoder) fail(message string) {
	if encoder.err == nil {
		encoder.err = fmt.Errorf("mutation IR canonical encoding: %s", message)
	}
}
func (encoder *canonicalEncoder) raw(value []byte) {
	if encoder.err == nil {
		_, encoder.err = encoder.buffer.Write(value)
	}
}
func (encoder *canonicalEncoder) u8(value uint8) { encoder.raw([]byte{value}) }
func (encoder *canonicalEncoder) u16(value uint16) {
	var data [2]byte
	binary.BigEndian.PutUint16(data[:], value)
	encoder.raw(data[:])
}
func (encoder *canonicalEncoder) u32(value uint32) {
	var data [4]byte
	binary.BigEndian.PutUint32(data[:], value)
	encoder.raw(data[:])
}
func (encoder *canonicalEncoder) u64(value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	encoder.raw(data[:])
}
func (encoder *canonicalEncoder) i64(value int64) { encoder.u64(uint64(value)) }
func (encoder *canonicalEncoder) boolean(value bool) {
	if value {
		encoder.u8(1)
	} else {
		encoder.u8(0)
	}
}
func (encoder *canonicalEncoder) count(length int) {
	if length < 0 || uint64(length) > math.MaxUint32 {
		encoder.fail("collection length exceeds uint32")
		return
	}
	encoder.u32(uint32(length))
}
func (encoder *canonicalEncoder) bytes(value []byte)  { encoder.count(len(value)); encoder.raw(value) }
func (encoder *canonicalEncoder) text(value string)   { encoder.bytes([]byte(value)) }
func (encoder *canonicalEncoder) id16(value [16]byte) { encoder.raw(value[:]) }

func (encoder *canonicalEncoder) node(node Node) {
	encoder.u8(uint8(node.operation))
	encoder.id16(node.model)
	encoder.id16(node.relation)
	encoder.u8(uint8(node.branch))
	encoder.u32(node.ordinal)
	encoder.boolean(node.hasParent)
	encoder.u32(node.parent)
	encoder.u16(node.depth)
	encoder.boolean(node.hasRelationAnchor)
	encoder.u32(node.relationAnchor)
	if node.target == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.target(*node.target)
	}
	if node.predicate == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.condition(*node.predicate)
	}
	if node.relationPosition == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.relationPosition(*node.relationPosition)
	}
	encoder.count(len(node.scalarOperations))
	for _, operation := range node.scalarOperations {
		encoder.scalar(operation)
	}
	encoder.fields(node.influencingFields)
	encoder.image(node.before)
	encoder.image(node.after)
	if node.selection == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.u8(uint8(node.selection.action))
		encoder.condition(node.selection.constraint)
	}
	if node.rowPostcondition == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.condition(*node.rowPostcondition)
	}
	encoder.count(len(node.fieldConditions))
	for _, authorization := range node.fieldConditions {
		encoder.id16(authorization.field)
		encoder.condition(authorization.condition)
	}
	encoder.count(len(node.hooks))
	for _, hook := range node.hooks {
		encoder.u8(uint8(hook.phase))
		encoder.u8(uint8(hook.operation))
	}
	encoder.fact(node.fact)
	encoder.u8(uint8(node.identity))
	encoder.count(len(node.children))
	for _, child := range node.children {
		encoder.u32(child)
	}
}

func (encoder *canonicalEncoder) relationPosition(position RelationPosition) {
	encoder.id16(position.parentModel)
	encoder.id16(position.field)
	encoder.id16(position.relation)
	encoder.id16(position.targetModel)
	encoder.u8(uint8(position.kind))
	if position.target == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.target(*position.target)
	}
	if position.predicate == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.condition(*position.predicate)
	}
	encoder.count(len(position.desired))
	for _, target := range position.desired {
		encoder.target(target)
	}
	if position.expansion == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.u8(uint8(position.expansion.kind))
		encoder.u32(position.expansion.maxRows)
	}
}

func (encoder *canonicalEncoder) target(target Target) {
	encoder.id16(target.model)
	encoder.id16(target.key)
	encoder.count(len(target.values))
	for _, value := range target.values {
		encoder.id16(value.field)
		encoder.value(value.value)
	}
	if target.guard == nil {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.condition(*target.guard)
	}
}

func (encoder *canonicalEncoder) scalar(operation ScalarOperation) {
	encoder.id16(operation.field)
	encoder.typeRef(operation.fieldType)
	encoder.u8(uint8(operation.kind))
	encoder.boolean(operation.runtimeOwned)
	encoder.boolean(operation.hasValue)
	if operation.hasValue {
		encoder.value(operation.value)
	}
}

func (encoder *canonicalEncoder) condition(condition policyir.Condition) {
	value, err := policyir.CanonicalCondition(condition)
	if err != nil {
		encoder.fail("invalid policy condition")
		return
	}
	encoder.bytes(value)
}

func (encoder *canonicalEncoder) image(image ImageRequirements) {
	encoder.id16(image.model)
	encoder.fields(image.fields)
	encoder.count(len(image.dependencies))
	for _, dependency := range image.dependencies {
		encoder.id16(dependency.root)
		encoder.count(len(dependency.path))
		for _, hop := range dependency.path {
			encoder.id16(hop.model)
			encoder.id16(hop.field)
			encoder.id16(hop.relation)
			encoder.id16(hop.target)
		}
		encoder.id16(dependency.field)
	}
}

func (encoder *canonicalEncoder) fields(fields []policyir.FieldID) {
	encoder.count(len(fields))
	for _, field := range fields {
		encoder.id16(field)
	}
}

func (encoder *canonicalEncoder) fact(fact FactRequirement) {
	encoder.boolean(fact.enabled)
	if !fact.enabled {
		return
	}
	encoder.u8(uint8(fact.action))
	if fact.eventSchema == ([32]byte{}) {
		encoder.u8(0)
	} else {
		encoder.u8(1)
		encoder.raw(fact.eventSchema[:])
	}
	encoder.fields(fact.beforeIdentity)
	encoder.fields(fact.afterIdentity)
	encoder.u8(uint8(fact.deleteSnapshotState))
	encoder.fields(fact.privateDeleteSnapshot)
}

func (encoder *canonicalEncoder) typeRef(value policyir.TypeRef) {
	encoder.u8(uint8(value.Kind()))
	encoder.boolean(value.Nullable())
	encoder.u16(value.Precision())
	encoder.u16(value.Scale())
	enum, _ := value.EnumID()
	encoder.id16(enum)
	encoder.u16(uint16(value.Capability()))
	if element, ok := value.Element(); ok {
		encoder.u8(1)
		encoder.typeRef(element)
	} else {
		encoder.u8(0)
	}
}

func (encoder *canonicalEncoder) value(value policyir.Value) {
	encoder.u8(uint8(value.Kind()))
	switch value.Kind() {
	case policyir.ValueBool:
		result, _ := value.Bool()
		encoder.boolean(result)
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		result, _ := value.Signed()
		encoder.i64(result)
	case policyir.ValueFloat32:
		result, _ := value.Float32Bits()
		encoder.u32(result)
	case policyir.ValueFloat64:
		result, _ := value.Float64Bits()
		encoder.u64(result)
	case policyir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		encoder.i64(coefficient)
		encoder.u8(scale)
	case policyir.ValueString:
		result, _ := value.Text()
		encoder.text(result)
	case policyir.ValueBytes:
		result, _ := value.Bytes()
		encoder.bytes(result)
	case policyir.ValueUUID:
		result, _ := value.UUID()
		encoder.raw(result[:])
	case policyir.ValueDate:
		year, month, day, _ := value.Date()
		encoder.u16(uint16(year))
		encoder.u8(month)
		encoder.u8(day)
	case policyir.ValueTime:
		result, _ := value.Time()
		encoder.i64(result)
	case policyir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		encoder.i64(seconds)
		encoder.u32(nanos)
	case policyir.ValueEnum:
		enum, member, _ := value.Enum()
		encoder.id16(enum)
		encoder.id16(member)
	case policyir.ValueJSON:
		result, _ := value.JSON()
		encoder.json(result)
	case policyir.ValueScalarList:
		result, _ := value.List()
		encoder.count(len(result))
		for _, item := range result {
			encoder.value(item)
		}
	default:
		encoder.fail("unknown logical value kind")
	}
}

func (encoder *canonicalEncoder) json(value policyir.JSONValue) {
	encoder.u8(uint8(value.Kind()))
	switch value.Kind() {
	case policyir.JSONNull:
	case policyir.JSONBool:
		result, _ := value.Bool()
		encoder.boolean(result)
	case policyir.JSONNumber:
		result, _ := value.Number()
		encoder.boolean(result.Negative())
		encoder.bytes(result.Coefficient())
		encoder.u32(uint32(result.Exponent()))
	case policyir.JSONString:
		result, _ := value.Text()
		encoder.text(result)
	case policyir.JSONArray:
		result, _ := value.Array()
		encoder.count(len(result))
		for _, item := range result {
			encoder.json(item)
		}
	case policyir.JSONObject:
		result, _ := value.Object()
		encoder.count(len(result))
		for _, member := range result {
			encoder.text(member.Key())
			encoder.json(member.Value())
		}
	default:
		encoder.fail("unknown JSON value kind")
	}
}
