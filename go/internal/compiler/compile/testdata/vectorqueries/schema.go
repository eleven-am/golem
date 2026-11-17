package vectorqueries

import "github.com/eleven-am/golem/go/golem"

type Actor struct {
	Viewer      string
	ReadContent bool
	HideOwner   bool
}

type Document struct {
	_         struct{}  `golem:"model;id=vectorqueries.Document;table=documents"`
	ID        string    `db:"golem_semantic_position" golem:"pk"`
	Owner     string    `db:"owner"`
	Visible   bool      `db:"visible"`
	Content   string    `db:"content" golem:"internal"`
	Credits   []Credit  `db:"-" golem:"relation=has_many;fields=golem_semantic_position;references=document_id"`
	Companies []Company `db:"-" golem:"relation=has_many;fields=golem_semantic_position;references=document_id"`
}

type Credit struct {
	_          struct{}  `golem:"model;id=vectorqueries.Credit;table=credits"`
	ID         string    `db:"id" golem:"pk"`
	DocumentID string    `db:"document_id"`
	Name       string    `db:"name"`
	Document   *Document `db:"-" golem:"relation=belongs_to;fields=document_id;references=golem_semantic_position"`
}

type Company struct {
	_          struct{}  `golem:"model;id=vectorqueries.Company;table=companies"`
	ID         string    `db:"id" golem:"pk"`
	DocumentID string    `db:"document_id"`
	Name       string    `db:"name"`
	Document   *Document `db:"-" golem:"relation=belongs_to;fields=document_id;references=golem_semantic_position"`
}

type Token struct {
	_       struct{}   `golem:"model;id=vectorqueries.Token;table=tokens"`
	ID      golem.UUID `db:"id" golem:"pk"`
	Content string     `db:"content"`
}

type Small struct {
	_       struct{} `golem:"model;id=vectorqueries.Small;table=smalls"`
	_       struct{} `golem:"primary=pk_small(id,serial)"`
	ID      int16    `db:"id"`
	Serial  int32    `db:"serial"`
	Content string   `db:"content"`
}

func (Token) GolemModel() golem.ModelSpec[Token] {
	return golem.DefineModel(golem.SemanticIndex("content", "content", Tokens.Content))
}
func (Small) GolemModel() golem.ModelSpec[Small] {
	return golem.DefineModel(golem.SemanticIndex("content", "content", Smalls.Content))
}
func (Token) DefinePolicy(rules *golem.Rules[Token], _ Actor) { rules.CanRead(golem.All[Token]()) }
func (Small) DefinePolicy(rules *golem.Rules[Small], _ Actor) { rules.CanRead(golem.All[Small]()) }

func (Credit) DefinePolicy(rules *golem.Rules[Credit], _ Actor) { rules.CanRead(golem.All[Credit]()) }
func (Company) DefinePolicy(rules *golem.Rules[Company], _ Actor) {
	rules.CanRead(golem.All[Company]())
}

func (Document) GolemModel() golem.ModelSpec[Document] {
	return golem.DefineModel(golem.SemanticIndex("content", "content", Documents.Content))
}

func (Document) DefinePolicy(rules *golem.Rules[Document], actor Actor) {
	owned := Documents.Owner.Eq(actor.Viewer)
	rules.CanRead(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
	rules.CannotReadFields(golem.All[Document](), Documents.Content)
	if actor.HideOwner {
		rules.CannotReadFields(golem.All[Document](), Documents.Owner)
	}
	if actor.ReadContent {
		rules.CanReadFields(owned.And(Documents.Visible.Eq(true)), Documents.Content)
	}
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "vector_queries")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[Document](schema)
	golem.Model[Credit](schema)
	golem.Model[Company](schema)
	golem.Model[Token](schema)
	golem.Model[Small](schema)
	golem.Providers(schema, golem.SQLite)
	golem.EmbeddingSpace(schema, "content", 3)
}
