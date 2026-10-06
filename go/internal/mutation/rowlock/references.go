package rowlock

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
)

type Reference struct {
	model  policyir.ModelID
	fields []policyir.FieldID
	values []policyir.Value
}

func References(registry *schema.Registry, model policyir.ModelID, writes []mutationir.ScalarOperation, before []mutationdecode.Row) ([]Reference, error) {
	written := make(map[policyir.FieldID]mutationir.ScalarOperation, len(writes))
	for _, operation := range writes {
		written[operation.FieldID()] = operation
	}
	images := before
	if len(images) == 0 {
		images = []mutationdecode.Row{{}}
	}
	var references []Reference
	for _, endpoint := range registry.ForeignKeys(golem.ModelID(model)) {
		touched := false
		for _, pair := range endpoint.Correlation() {
			if _, present := written[policyir.FieldID(pair.ParentFieldID())]; present {
				touched = true
			}
		}
		if !touched {
			continue
		}
		for _, image := range images {
			reference := Reference{model: policyir.ModelID(endpoint.TargetModelID())}
			complete := true
			for _, pair := range endpoint.Correlation() {
				local := policyir.FieldID(pair.ParentFieldID())
				value, present, err := referencedValue(local, written, image, len(before) != 0)
				if err != nil {
					return nil, err
				}
				if !present {
					complete = false
					break
				}
				reference.fields = append(reference.fields, policyir.FieldID(pair.ChildFieldID()))
				reference.values = append(reference.values, value)
			}
			if complete {
				references = append(references, reference)
			}
		}
	}
	return references, nil
}

func referencedValue(field policyir.FieldID, written map[policyir.FieldID]mutationir.ScalarOperation, image mutationdecode.Row, imaged bool) (policyir.Value, bool, error) {
	if operation, present := written[field]; present {
		value, valued := operation.Value()
		if operation.Kind() == mutationir.ScalarSet && valued {
			return value, true, nil
		}
		if operation.Kind() == mutationir.ScalarNull {
			return policyir.Value{}, false, nil
		}
		return policyir.Value{}, false, fmt.Errorf("P4_ROW_LOCK_INVARIANT: foreign-key field %x reached the ledger with arithmetic, which the binder refuses", field)
	}
	if !imaged {
		return policyir.Value{}, false, nil
	}
	cell, present := image.Cell(field)
	if !present {
		return policyir.Value{}, false, fmt.Errorf("P4_ROW_LOCK_REFERENCE: foreign-key field %x is absent from the locked image", field)
	}
	if cell.IsNull() {
		return policyir.Value{}, false, nil
	}
	value, valued := cell.PolicyValue()
	if !valued {
		return policyir.Value{}, false, fmt.Errorf("P4_ROW_LOCK_REFERENCE: foreign-key field %x has no exact value", field)
	}
	return value, true, nil
}

func (session Session) LockReferences(ctx context.Context, references []Reference) error {
	if len(references) == 0 || session.Provider != policyir.ProviderPostgreSQL {
		return nil
	}
	var direct []Key
	var indirect []Reference
	for _, reference := range references {
		key, primary, err := primaryReferenceKey(session.Registry, reference)
		if err != nil {
			return err
		}
		if primary {
			direct = append(direct, key)
			continue
		}
		indirect = append(indirect, reference)
	}
	_, err := Select(ctx, session, func(ctx context.Context, _ func(Key) bool) (struct{}, []Key, error) {
		keys := append([]Key(nil), direct...)
		for _, reference := range indirect {
			found, err := session.referencedKeys(ctx, reference)
			if err != nil {
				return struct{}{}, nil, err
			}
			keys = append(keys, found...)
		}
		return struct{}{}, keys, nil
	})
	return err
}

func primaryReferenceKey(registry *schema.Registry, reference Reference) (Key, bool, error) {
	model, ok := registry.Model(golem.ModelID(reference.model))
	if !ok {
		return Key{}, false, fmt.Errorf("P4_ROW_LOCK_REFERENCE: referenced model %x is absent", reference.model)
	}
	var primary schema.Identity
	found := false
	for _, identity := range model.Identities() {
		if identity.Kind() == compilerir.KeyPrimary {
			primary, found = identity, true
		}
	}
	if !found {
		return Key{}, false, fmt.Errorf("P4_ROW_LOCK_REFERENCE: referenced model %x has no primary key", reference.model)
	}
	byField := make(map[policyir.FieldID]policyir.Value, len(reference.fields))
	for index, field := range reference.fields {
		byField[field] = reference.values[index]
	}
	if len(primary.Fields()) != len(byField) {
		return Key{}, false, nil
	}
	components := make([]mutationdecode.IdentityComponent, len(primary.Fields()))
	for index, public := range primary.Fields() {
		value, present := byField[policyir.FieldID(public)]
		if !present {
			return Key{}, false, nil
		}
		component, err := mutationdecode.IdentityValue(policyir.FieldID(public), value)
		if err != nil {
			return Key{}, false, fmt.Errorf("P4_ROW_LOCK_REFERENCE: %w", err)
		}
		components[index] = component
	}
	identity, err := mutationdecode.NewIdentity(primary.KeyID(), components)
	if err != nil {
		return Key{}, false, fmt.Errorf("P4_ROW_LOCK_REFERENCE: %w", err)
	}
	key, err := identityKey(reference.model, identity, KeyShare)
	return key, true, err
}

func (session Session) referencedKeys(ctx context.Context, reference Reference) ([]Key, error) {
	model, ok := session.Registry.Model(golem.ModelID(reference.model))
	if !ok {
		return nil, fmt.Errorf("P4_ROW_LOCK_REFERENCE: referenced model %x is absent", reference.model)
	}
	resolver := policysql.SchemaResolver(session.Registry)
	physicalModel, ok := resolver.Model(session.Provider, reference.model)
	if !ok {
		return nil, fmt.Errorf("P4_ROW_LOCK_SCHEMA: referenced model %x has no physical descriptor", reference.model)
	}
	dialect := postgresprovider.NewPolicyDialect()
	primary := make([]policyir.FieldID, len(model.PrimaryKey()))
	columns := make([]string, len(primary))
	for index, public := range model.PrimaryKey() {
		primary[index] = policyir.FieldID(public)
		field, found := resolver.Field(session.Provider, reference.model, primary[index])
		if !found {
			return nil, fmt.Errorf("P4_ROW_LOCK_SCHEMA: referenced primary-key field %x has no physical descriptor", primary[index])
		}
		columns[index] = policysql.ProjectColumn(session.Provider, field.Type, dialect.Quote(lockAlias)+"."+dialect.Quote(field.Column))
	}
	args := make([]any, len(reference.fields))
	parts := make([]string, len(reference.fields))
	for index, fieldID := range reference.fields {
		field, found := resolver.Field(session.Provider, reference.model, fieldID)
		if !found {
			return nil, fmt.Errorf("P4_ROW_LOCK_SCHEMA: referenced field %x has no physical descriptor", fieldID)
		}
		encoded, err := encode(dialect, resolver, field.Type, reference.values[index])
		if err != nil {
			return nil, fmt.Errorf("P4_ROW_LOCK_REFERENCE: referenced value cannot be encoded: %w", err)
		}
		args[index] = encoded
		parts[index] = dialect.Quote(lockAlias) + "." + dialect.Quote(field.Column) + " = " + dialect.Placeholder(index+1)
	}
	text := "SELECT " + strings.Join(columns, ", ") + " FROM " + dialect.Table(physicalModel) + " AS " + dialect.Quote(lockAlias) + " WHERE " + strings.Join(parts, " AND ")
	decoder, err := readdecode.NewFields(reference.model, session.Registry, session.Provider, primary)
	if err != nil {
		return nil, err
	}
	rows, err := session.Queryer.QueryxContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []Key
	for rows.Next() {
		scan := decoder.NewScan()
		if err := rows.Scan(scan.Destinations()...); err != nil {
			return nil, err
		}
		cells, err := scan.Decode()
		if err != nil {
			return nil, err
		}
		row, err := mutationdecode.FromReadCells(session.Registry, reference.model, cells)
		if err != nil {
			return nil, err
		}
		key, err := RowKey(session.Registry, row)
		if err != nil {
			return nil, err
		}
		key.mode = KeyShare
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].order < keys[j].order })
	return keys, rows.Close()
}
