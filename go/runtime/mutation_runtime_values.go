package runtime

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// mutationRuntimeValues freezes application-produced values once for one
// logical mutation. A prepared upsert reuses the resulting operations for all
// engine-owned retries, so retries cannot silently change UUIDs or timestamps.
type mutationRuntimeValues struct {
	now    time.Time
	random io.Reader
	uuids  map[mutationRuntimeValueKey][16]byte
}

type mutationRuntimeValueKey struct {
	model policyir.ModelID
	field policyir.FieldID
	slot  uint32
}

func newMutationRuntimeValues() *mutationRuntimeValues {
	return newMutationRuntimeValuesFrom(time.Now(), rand.Reader)
}

func newMutationRuntimeValuesFrom(now time.Time, random io.Reader) *mutationRuntimeValues {
	return &mutationRuntimeValues{
		now:    now.UTC(),
		random: random,
		uuids:  make(map[mutationRuntimeValueKey][16]byte),
	}
}

func (values *mutationRuntimeValues) apply(input mutationbind.ScalarInput, registry *schema.Registry) (mutationbind.ScalarInput, error) {
	return values.applyAt(input, registry, 0)
}

func (values *mutationRuntimeValues) applyAt(input mutationbind.ScalarInput, registry *schema.Registry, slot uint32) (mutationbind.ScalarInput, error) {
	resolved, err := values.resolve(input.ModelID(), input.Kind(), input.Operations(), registry, slot)
	if err != nil {
		return mutationbind.ScalarInput{}, err
	}
	bound, err := mutationbind.WithRuntimeOwnedValues(input, registry, resolved)
	if err != nil {
		return mutationbind.ScalarInput{}, err
	}
	if input.Kind() == mutationbind.InputCreate {
		return mutationbind.WithOptimisticConcurrencyCreate(bound, registry)
	}
	return bound, nil
}

func (values *mutationRuntimeValues) applyOperations(model policyir.ModelID, kind mutationbind.InputKind, operations []mutationir.ScalarOperation, registry *schema.Registry, slot uint32) ([]mutationir.ScalarOperation, error) {
	resolved, err := values.resolve(model, kind, operations, registry, slot)
	if err != nil {
		return nil, err
	}
	return mutationbind.WithRuntimeOwnedOperations(model, kind, operations, registry, resolved)
}

func (values *mutationRuntimeValues) resolve(modelID policyir.ModelID, kind mutationbind.InputKind, operations []mutationir.ScalarOperation, registry *schema.Registry, slot uint32) ([]mutationbind.RuntimeOwnedValue, error) {
	if values == nil || registry == nil {
		return nil, fmt.Errorf("P4_RUNTIME_VALUES: runtime value snapshot and schema registry are required")
	}
	model, ok := registry.Model(golem.ModelID(modelID))
	if !ok {
		return nil, fmt.Errorf("P4_RUNTIME_VALUES: mutation model is absent from the active schema")
	}
	present := make(map[policyir.FieldID]struct{}, len(operations))
	for _, operation := range operations {
		present[operation.FieldID()] = struct{}{}
	}
	resolved := make([]mutationbind.RuntimeOwnedValue, 0)
	for _, publicFieldID := range model.Fields() {
		fieldID := policyir.FieldID(publicFieldID)
		if _, exists := present[fieldID]; exists {
			continue
		}
		field, exists := registry.Field(model.ID(), publicFieldID)
		if !exists {
			continue
		}
		var (
			value    policyir.Value
			produced bool
			err      error
		)
		if kind == mutationbind.InputCreate {
			if defaultValue, hasDefault := field.Default(); hasDefault && defaultValue.Producer == compilerir.ProducerApplication {
				value, err = values.defaultValue(modelID, fieldID, slot, field.LogicalType(), defaultValue)
				produced = err == nil
			}
		}
		if !produced && err == nil && field.Updated() {
			value, err = values.nowValue(field.LogicalType())
			produced = err == nil
		}
		if err != nil {
			return nil, fmt.Errorf("P4_RUNTIME_VALUES: model=%x field=%x: %w", modelID, fieldID, err)
		}
		if produced {
			resolved = append(resolved, mutationbind.RuntimeOwnedValue{Field: fieldID, Value: value})
		}
	}
	return resolved, nil
}

func (values *mutationRuntimeValues) defaultValue(model policyir.ModelID, field policyir.FieldID, slot uint32, typ compilerir.LogicalTypeIR, defaultValue compilerir.DefaultIR) (policyir.Value, error) {
	switch defaultValue.Kind {
	case compilerir.DefaultUUID:
		uuid, err := values.uuid(model, field, slot)
		if err != nil {
			return policyir.Value{}, err
		}
		switch typ.Kind {
		case compilerir.TypeUUID:
			return policyir.UUIDValue(uuid), nil
		case compilerir.TypeString:
			if typ.MaxLength != nil && *typ.MaxLength < 36 {
				return policyir.Value{}, fmt.Errorf("uuid string default requires max length of at least 36")
			}
			return policyir.StringValue(formatUUID(uuid))
		default:
			return policyir.Value{}, fmt.Errorf("uuid default has unsupported logical type %q", typ.Kind)
		}
	case compilerir.DefaultNow:
		return values.nowValue(typ)
	default:
		return policyir.Value{}, fmt.Errorf("application default kind %q is unsupported", defaultValue.Kind)
	}
}

func (values *mutationRuntimeValues) uuid(model policyir.ModelID, field policyir.FieldID, slot uint32) ([16]byte, error) {
	key := mutationRuntimeValueKey{model: model, field: field, slot: slot}
	if value, ok := values.uuids[key]; ok {
		return value, nil
	}
	var value [16]byte
	if values.random == nil {
		return value, fmt.Errorf("uuid entropy source is unavailable")
	}
	if _, err := io.ReadFull(values.random, value[:]); err != nil {
		return [16]byte{}, fmt.Errorf("uuid generation failed: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	values.uuids[key] = value
	return value, nil
}

func (values *mutationRuntimeValues) nowValue(typ compilerir.LogicalTypeIR) (policyir.Value, error) {
	now := truncateRuntimeTime(values.now, temporalPrecision(typ))
	switch typ.Kind {
	case compilerir.TypeDate:
		return policyir.NewDateValue(int16(now.Year()), uint8(now.Month()), uint8(now.Day()))
	case compilerir.TypeTime:
		microseconds := int64(now.Hour())*3_600_000_000 + int64(now.Minute())*60_000_000 + int64(now.Second())*1_000_000 + int64(now.Nanosecond()/1_000)
		return policyir.NewTimeValue(microseconds)
	case compilerir.TypeDateTime:
		return policyir.NewDateTimeValue(now.Unix(), uint32(now.Nanosecond()))
	default:
		return policyir.Value{}, fmt.Errorf("now value has unsupported logical type %q", typ.Kind)
	}
}

func temporalPrecision(typ compilerir.LogicalTypeIR) uint16 {
	if typ.Kind == compilerir.TypeDate {
		return 0
	}
	if typ.Precision == nil || *typ.Precision > 6 {
		return 6
	}
	return *typ.Precision
}

func truncateRuntimeTime(value time.Time, precision uint16) time.Time {
	quantum := int64(1)
	for index := precision; index < 9; index++ {
		quantum *= 10
	}
	nanos := int64(value.Nanosecond())
	return time.Unix(value.Unix(), nanos/quantum*quantum).UTC()
}

func formatUUID(value [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
