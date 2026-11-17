package policyfacts

import "github.com/eleven-am/golem/go/golem"

type Actor struct{}
type EmptyArgs struct{}
type FactArgs struct {
	States    []string
	MaxHeight int64
}

type Media struct {
	_      struct{}   `golem:"model;table=media"`
	ID     golem.UUID `db:"id" golem:"pk"`
	Videos []Video    `db:"-" golem:"relation=has_many;fields=id;references=media_id"`
}

type Video struct {
	_              struct{}        `golem:"model;table=videos"`
	ID             golem.UUID      `db:"id" golem:"pk"`
	MediaID        golem.UUID      `db:"media_id"`
	Height         int64           `db:"height"`
	Media          *Media          `db:"-" golem:"relation=belongs_to;fields=media_id;references=id"`
	StorageObjects []StorageObject `db:"-" golem:"relation=has_many;fields=id;references=video_id"`
}

type StorageObject struct {
	_       struct{}   `golem:"model;table=storage_objects"`
	ID      golem.UUID `db:"id" golem:"pk"`
	VideoID golem.UUID `db:"video_id"`
	State   string     `db:"state" golem:"type=varchar(20)"`
	Video   *Video     `db:"-" golem:"relation=belongs_to;fields=video_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "policy_fact_templates")
	golem.Actor[Actor](schema)
	golem.Model[Media](schema)
	golem.Model[Video](schema)
	golem.Model[StorageObject](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
	golem.WithoutGraphQL(schema)
}

func (Media) GolemModel() golem.ModelSpec[Media] {
	return golem.DefineModel(golem.Subscriptions(
		golem.ScopeTemplate("owner", func(args EmptyArgs) golem.Predicate[Media] { return golem.All[Media]() }),
		golem.FactTemplate("ready", "owner", func(args FactArgs) golem.Predicate[Media] {
			return Medias.Videos.None(Videos.Height.GT(args.MaxHeight).Or(
				Videos.StorageObjects.Every(StorageObjects.State.NotIn(args.States...)).Not(),
			))
		}),
	))
}

func (Video) GolemModel() golem.ModelSpec[Video] {
	return golem.DefineModel(golem.Subscriptions[Video]())
}
func (StorageObject) GolemModel() golem.ModelSpec[StorageObject] {
	return golem.DefineModel(golem.Subscriptions[StorageObject]())
}

func (Media) DefinePolicy(rules *golem.Rules[Media], actor Actor) {
	rules.BindFactScope(MediaScopes.Owner(EmptyArgs{}))
	ready := rules.BindFact(MediaFacts.Ready(FactArgs{States: []string{"ready"}, MaxHeight: 10}))
	rules.CanRead(ready.Not())
}
