package operation

import (
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type HydratedRows map[golem.RuntimeSemanticIdentityToken]golem.RuntimeModelRow

type Hydration struct {
	typ         compilerir.GraphQLTypeIR
	publicModel golem.ModelID
	model       policyir.ModelID
	selection   []readir.Selection
	where       *golem.FrozenPredicate
	distinct    bool
	chunk       int
	identities  []golem.RuntimeRowIdentity
	requested   map[golem.RuntimeSemanticIdentityToken]struct{}
}

func HoldsModel(typ compilerir.GraphQLTypeIR) bool { return customResultModelName(typ) != "" }

func (c *Compiler) CustomHydration(root CustomRoot) (*Hydration, bool, error) {
	if c == nil {
		return nil, false, fmt.Errorf("custom hydration compiler is absent")
	}
	if !HoldsModel(root.Result) {
		return nil, false, nil
	}
	var where *golem.FrozenPredicate
	if root.semantic != nil {
		where = root.semantic.where
	}
	hydration, err := c.newHydration(root.Result, root.model, root.selection, where, root.semantic != nil)
	return hydration, err == nil, err
}

func (c *Compiler) ComputedHydration(slot selectset.Slot) (*Hydration, bool, error) {
	if c == nil || slot.Computed == nil {
		return nil, false, fmt.Errorf("computed hydration metadata is absent")
	}
	if !HoldsModel(slot.Computed.Result) {
		return nil, false, nil
	}
	name := customResultModelName(slot.Computed.Result)
	modelID, ok := c.modelIDByGraphQLName(name)
	if !ok {
		return nil, false, fmt.Errorf("computed model type %s is absent", name)
	}
	hydration, err := c.newHydration(slot.Computed.Result, modelID, slot.Computed.Projection, nil, false)
	return hydration, err == nil, err
}

func (c *Compiler) newHydration(typ compilerir.GraphQLTypeIR, modelID compilerir.ModelID, selection []readir.Selection, where *golem.FrozenPredicate, distinct bool) (*Hydration, error) {
	publicModel, err := publicModelID(modelID)
	if err != nil {
		return nil, err
	}
	contract, ok := c.modelContractByID(modelID)
	if !ok {
		return nil, fmt.Errorf("hydration model contract is absent")
	}
	chunk := readir.NarrowCap(0, int(contract.Limits.MaxPageSize))
	chunk = readir.NarrowCap(chunk, int(contract.Limits.MaxTake))
	chunk = readir.NarrowCap(chunk, c.limits.Bind.MaxPageSize)
	return &Hydration{
		typ: typ, publicModel: publicModel, model: policyir.ModelID(publicModel), selection: append([]readir.Selection(nil), selection...),
		where: where, distinct: distinct, chunk: chunk, requested: map[golem.RuntimeSemanticIdentityToken]struct{}{},
	}, nil
}

func (h *Hydration) Len() int { return len(h.identities) }

func (h *Hydration) Chunk() int {
	if h.chunk <= 0 || h.chunk > len(h.identities) {
		return len(h.identities)
	}
	return h.chunk
}

func (h *Hydration) Collect(value any) error {
	_, _, err := h.walk(h.typ, value, func(row golem.RuntimeModelRow) (golem.RuntimeModelRow, bool, error) {
		identity, err := h.identityOf(row)
		if err != nil {
			return row, false, err
		}
		token := identity.Token()
		if _, seen := h.requested[token]; seen {
			if h.distinct {
				return row, false, fmt.Errorf("semantic search returned a duplicate identity")
			}
			return row, true, nil
		}
		h.requested[token] = struct{}{}
		h.identities = append(h.identities, identity)
		return row, true, nil
	})
	return err
}

func (h *Hydration) Index(rows []golem.RuntimeModelRow) (HydratedRows, error) {
	result := make(HydratedRows, len(rows))
	for index, row := range rows {
		identity, err := h.identityOf(row)
		if err != nil {
			return nil, fmt.Errorf("hydrated item %d: %w", index, err)
		}
		token := identity.Token()
		if _, requested := h.requested[token]; !requested {
			return nil, fmt.Errorf("hydrated item %d was not requested", index)
		}
		if _, duplicate := result[token]; duplicate {
			return nil, fmt.Errorf("hydration returned a duplicate identity")
		}
		result[token] = row
	}
	return result, nil
}

func (h *Hydration) Restore(value any, rows HydratedRows) (any, int, error) {
	absent := 0
	restored, keep, err := h.walk(h.typ, value, func(row golem.RuntimeModelRow) (golem.RuntimeModelRow, bool, error) {
		identity, err := h.identityOf(row)
		if err != nil {
			return row, false, err
		}
		hydrated, present := rows[identity.Token()]
		if !present {
			absent++
			return row, false, nil
		}
		return hydrated, true, nil
	})
	if err != nil {
		return nil, 0, err
	}
	if !keep {
		return nil, absent, nil
	}
	return restored, absent, nil
}

func (c *Compiler) HydrationRequest(h *Hydration, start, end int) (golem.FrozenReadRequest, error) {
	if c == nil || h == nil || start < 0 || end <= start || end > len(h.identities) {
		return golem.FrozenReadRequest{}, fmt.Errorf("hydration range is invalid")
	}
	take := end - start
	request, err := readir.NewRequest(readir.RequestInput{
		Operation: readir.FindMany, Model: h.model,
		Take: &take, Selection: h.selection, Projection: readir.ProjectionSelect,
	})
	if err != nil {
		return golem.FrozenReadRequest{}, err
	}
	base, err := c.freezeRequest(request)
	if err != nil {
		return golem.FrozenReadRequest{}, err
	}
	if h.where != nil {
		return golem.RuntimeIdentityHydrationRequest(base, h.identities[start:end], *h.where)
	}
	return golem.RuntimeIdentityHydrationRequest(base, h.identities[start:end])
}

func (h *Hydration) AbsentIsMissing(absent int) bool {
	return absent > 0 && h.typ.Kind != compilerir.GraphQLTypeList && !h.typ.Nullable
}

func (h *Hydration) identityOf(row golem.RuntimeModelRow) (golem.RuntimeRowIdentity, error) {
	if row.ModelID() != h.publicModel {
		return golem.RuntimeRowIdentity{}, fmt.Errorf("result row is not the declared model")
	}
	identity := golem.RuntimeModelRowIdentity(row)
	if !identity.Present() {
		return golem.RuntimeRowIdentity{}, fmt.Errorf("result row has no identity")
	}
	return identity, nil
}

type hydrationVisit func(golem.RuntimeModelRow) (golem.RuntimeModelRow, bool, error)

func (h *Hydration) walk(typ compilerir.GraphQLTypeIR, value any, visit hydrationVisit) (any, bool, error) {
	if value == nil {
		return nil, true, nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return nil, true, nil
		}
		reflected = reflected.Elem()
		value = reflected.Interface()
	}
	switch typ.Kind {
	case compilerir.GraphQLTypeList:
		if typ.Element == nil || reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
			return nil, false, fmt.Errorf("hydrated list has value %T", value)
		}
		items := make([]any, 0, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			item, keep, err := h.walk(*typ.Element, reflected.Index(index).Interface(), visit)
			if err != nil {
				return nil, false, fmt.Errorf("item %d: %w", index, err)
			}
			if keep {
				items = append(items, item)
			}
		}
		return items, true, nil
	case compilerir.GraphQLTypeModel:
		row, ok := value.(golem.RuntimeModelRow)
		if !ok {
			return nil, false, fmt.Errorf("hydrated model has value %T", value)
		}
		replaced, keep, err := visit(row)
		if err != nil {
			return nil, false, err
		}
		return replaced, keep, nil
	default:
		return value, true, nil
	}
}

func (c *Compiler) modelContractByID(id compilerir.ModelID) (compilerir.ModelContractIR, bool) {
	for _, model := range c.compilation.Contract.Models {
		if model.ModelID == id {
			return model, true
		}
	}
	return compilerir.ModelContractIR{}, false
}
