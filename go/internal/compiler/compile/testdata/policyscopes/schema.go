package policyscopes

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ StorageIDs []golem.UUID }
type LibraryScopeArgs struct{ StorageIDs []golem.UUID }

type Media struct {
	_      struct{}   `golem:"model;table=media"`
	ID     golem.UUID `db:"id" golem:"pk"`
	Videos []Video    `db:"-" golem:"relation=has_many;fields=id;references=media_id"`
}

type Video struct {
	_              struct{}        `golem:"model;table=videos"`
	ID             golem.UUID      `db:"id" golem:"pk"`
	MediaID        golem.UUID      `db:"media_id"`
	StorageID      golem.UUID      `db:"storage_id"`
	Media          *Media          `db:"-" golem:"relation=belongs_to;fields=media_id;references=id"`
	StorageObjects []StorageObject `db:"-" golem:"relation=has_many;fields=id;references=video_id"`
}

type StorageObject struct {
	_         struct{}   `golem:"model;table=storage_objects"`
	ID        golem.UUID `db:"id" golem:"pk"`
	VideoID   golem.UUID `db:"video_id"`
	StorageID golem.UUID `db:"storage_id"`
	State     string     `db:"state" golem:"type=varchar(20)"`
	Video     *Video     `db:"-" golem:"relation=belongs_to;fields=video_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "policy_scope_templates")
	golem.Actor[Actor](schema)
	golem.Model[Media](schema)
	golem.Model[Video](schema)
	golem.Model[StorageObject](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
	golem.WithoutGraphQL(schema)
}

func (Media) GolemModel() golem.ModelSpec[Media] {
	return golem.DefineModel(golem.Subscriptions(
		golem.ScopeTemplate("libraries", func(args LibraryScopeArgs) golem.Predicate[Media] {
			return Medias.Videos.Some(Videos.StorageID.In(args.StorageIDs...).Or(
				Videos.StorageObjects.Some(StorageObjects.StorageID.In(args.StorageIDs...).And(StorageObjects.State.In("present", "missing"))),
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
	rules.BindFactScope(MediaScopes.Libraries(LibraryScopeArgs{StorageIDs: actor.StorageIDs}))
	ready := rules.Fact("ready", golem.All[Media]())
	rules.CanRead(ready)
}
