package hookvalues

import (
	"context"
	"sync"
	"time"

	golem "github.com/eleven-am/golem/go/golem"
)

type Principal struct{}

type Actor struct{}

type Kind string

const (
	KindAlpha Kind = "alpha"
	KindOmega Kind = "omega"
)

func (Kind) GolemEnum() golem.EnumSpec[Kind] {
	return golem.DefineEnum(golem.EnumValue(KindAlpha), golem.EnumValue(KindOmega))
}

type Owner struct {
	_ struct{} `golem:"model;id=hookvalues.Owner;table=owners;graphql=Owner"`

	ID      golem.UUID `db:"id" golem:"id=hookvalues.Owner.ID;pk"`
	Name    string     `db:"name" golem:"type=varchar(120)"`
	Entries []Entry    `db:"-" golem:"relation=has_many;fields=id;references=owner_id"`
}

func (Owner) GolemModel() golem.ModelSpec[Owner] {
	return golem.DefineModel(
		golem.GraphQL[Owner](golem.GraphQLOperations(golem.GraphQLFindOne, golem.GraphQLCreate, golem.GraphQLUpdate)),
	)
}

type Entry struct {
	_ struct{} `golem:"model;id=hookvalues.Entry;table=entries;graphql=Entry"`

	ID      golem.UUID             `db:"id" golem:"id=hookvalues.Entry.ID;pk"`
	OwnerID golem.Null[golem.UUID] `db:"owner_id"`
	Text    string                 `db:"text" golem:"type=varchar(120)"`
	Flag    bool                   `db:"flag"`
	Small   int16                  `db:"small"`
	Integer int32                  `db:"integer_value"`
	Big     int64                  `db:"big_value"`
	Single  float32                `db:"single_value"`
	Double  float64                `db:"double_value"`
	Amount  golem.Decimal          `db:"amount" golem:"type=decimal(18,4)"`
	Ref     golem.UUID             `db:"ref"`
	Day     golem.Date             `db:"day"`
	Clock   golem.Time             `db:"clock"`
	Instant time.Time              `db:"instant"`
	Payload []byte                 `db:"payload"`
	State   Kind                   `db:"state"`
	Tags    golem.List[string]     `db:"tags"`
	States  golem.List[Kind]       `db:"states"`
	Nick    golem.Null[string]     `db:"nick" golem:"type=varchar(120)"`
	Rank    golem.Null[int64]      `db:"rank"`
	Owner   *Owner                 `db:"-" golem:"relation=belongs_to;fields=owner_id;references=id"`
}

func (Entry) GolemModel() golem.ModelSpec[Entry] {
	return golem.DefineModel(
		golem.GraphQL[Entry](golem.GraphQLOperations(golem.GraphQLFindOne, golem.GraphQLCreate, golem.GraphQLUpdate)),
	)
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "hook_values")
	golem.Actor[Actor](schema)
	golem.Model[Owner](schema)
	golem.Model[Entry](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (Owner) DefinePolicy(rules *golem.Rules[Owner], _ Actor) {
	rules.CanRead(golem.All[Owner]())
	rules.CanCreate(golem.All[Owner]())
	rules.CanUpdate(golem.All[Owner]())
}

func (Entry) DefinePolicy(rules *golem.Rules[Entry], _ Actor) {
	rules.CanRead(golem.All[Entry]())
	rules.CanCreate(golem.All[Entry]())
	rules.CanUpdate(golem.All[Entry]())
}

type Observation struct {
	Hook      string
	Field     golem.FieldID
	Operation golem.MutationFieldOperation
	Value     any
	HasValue  bool
}

var (
	observationLock sync.Mutex
	observations    []Observation
)

func ResetObservations() {
	observationLock.Lock()
	defer observationLock.Unlock()
	observations = nil
}

func Observations() []Observation {
	observationLock.Lock()
	defer observationLock.Unlock()
	return append([]Observation(nil), observations...)
}

func recordInput(hook string, input golem.FrozenMutationInput) {
	observationLock.Lock()
	defer observationLock.Unlock()
	for _, field := range input.Fields() {
		value, present := field.Value()
		observations = append(observations, Observation{Hook: hook, Field: field.FieldID(), Operation: field.Operation(), Value: value, HasValue: present})
	}
}

func (Entry) BeforeCreate(_ context.Context, request *EntryCreateRequest) error {
	frozen, err := golem.RuntimeFreezeCreateInput(request.Input())
	if err != nil {
		return err
	}
	recordInput("entry.create", frozen)
	return nil
}

func (Entry) BeforeUpdate(_ context.Context, request *EntryUpdateRequest) error {
	frozen, err := golem.RuntimeFreezeUpdateInput(request.Input())
	if err != nil {
		return err
	}
	recordInput("entry.update", frozen)
	return nil
}
