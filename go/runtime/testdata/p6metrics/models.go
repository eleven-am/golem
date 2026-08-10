package p6metrics

import (
	"time"

	golem "github.com/eleven-am/golem/go/golem"
)

type Status string

const (
	StatusAlpha Status = "alpha"
	StatusOmega Status = "omega"
)

func (Status) GolemEnum() golem.EnumSpec[Status] {
	return golem.DefineEnum(golem.EnumValue(StatusAlpha), golem.EnumValue(StatusOmega))
}

type Metric struct {
	_ struct{} `golem:"model;id=p6metrics.Metric;table=p6_metrics;graphql=Metric"`

	ID              golem.UUID                `db:"id" golem:"id=p6metrics.Metric.ID;pk"`
	Flag            bool                      `db:"flag"`
	Small           int16                     `db:"small"`
	Integer         int32                     `db:"integer_value"`
	Big             int64                     `db:"big_value"`
	Float           float32                   `db:"float_value"`
	Double          float64                   `db:"double_value"`
	Amount          golem.Decimal             `db:"amount" golem:"type=decimal(18,4)"`
	Label           string                    `db:"label" golem:"type=varchar(120)"`
	Reference       golem.UUID                `db:"reference"`
	Day             golem.Date                `db:"day"`
	Clock           golem.Time                `db:"clock"`
	OccurredAt      time.Time                 `db:"occurred_at"`
	State           Status                    `db:"state"`
	OptionalBig     golem.Null[int64]         `db:"optional_big"`
	OptionalAmount  golem.Null[golem.Decimal] `db:"optional_amount" golem:"type=decimal(18,4)"`
	OptionalLabel   golem.Null[string]        `db:"optional_label" golem:"type=varchar(120)"`
	OptionalDay     golem.Null[golem.Date]    `db:"optional_day"`
	OptionalClock   golem.Null[golem.Time]    `db:"optional_clock"`
	OptionalInstant golem.Null[time.Time]     `db:"optional_instant"`
	CategoryID      golem.Null[golem.UUID]    `db:"category_id"`
	Category        *Category                 `db:"-" golem:"relation=belongs_to;fields=category_id;references=id"`
}

func (Metric) GolemModel() golem.ModelSpec[Metric] {
	return golem.DefineModel(
		golem.ScopedReads[Metric](),
		golem.Analytics[Metric](
			golem.AnalyticsRelationDimensions(
				golem.NamedRelationDimension("categoryParentName", golem.Via(Metrics.Category, golem.Via(Categories.Parent, golem.DimensionField(Categories.Name)))),
			),
			golem.AnalyticsLimits[Metric](100, 10_000),
		),
		golem.GraphQL[Metric](golem.GraphQLOperations(golem.GraphQLCreate, golem.GraphQLAggregate, golem.GraphQLGroupBy)),
	)
}

type Category struct {
	_ struct{} `golem:"model;id=p6metrics.Category;table=p6_categories;graphql=Category"`

	ID       golem.UUID             `db:"id" golem:"id=p6metrics.Category.ID;pk"`
	Name     string                 `db:"name" golem:"type=varchar(120)"`
	ParentID golem.Null[golem.UUID] `db:"parent_id"`
	Parent   *Category              `db:"-" golem:"relation=belongs_to;name=CategoryTree;fields=parent_id;references=id"`
	Children []Category             `db:"-" golem:"relation=has_many;name=CategoryTree;fields=id;references=parent_id"`
	Metrics  []Metric               `db:"-" golem:"relation=has_many;fields=id;references=category_id"`
}

func (Category) GolemModel() golem.ModelSpec[Category] {
	return golem.DefineModel(golem.GraphQL[Category](golem.GraphQLOperations(golem.GraphQLCreate)))
}
