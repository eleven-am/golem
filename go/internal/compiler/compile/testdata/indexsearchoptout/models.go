package indexsearchoptout

import "github.com/eleven-am/golem/go/golem"

type Document struct {
	_ struct{} `golem:"model;table=documents;graphql=Document"`

	ID   golem.UUID `db:"id" golem:"pk;default=uuid"`
	Body string     `db:"body"`
}

func (Document) GolemModel() golem.ModelSpec[Document] {
	return golem.DefineModel(
		golem.GraphQL[Document](
			golem.GraphQLOperations(golem.GraphQLFindOne, golem.GraphQLFindMany),
			golem.GraphQLWithoutIndexSearch(),
		),
		golem.FullTextIndex("content", golem.FullTextField(Documents.Body, 1)),
		golem.SemanticIndex("meaning", "content", Documents.Body),
	)
}

func (Document) DefinePolicy(rules *golem.Rules[Document], _ Actor) {
	rules.CanRead(golem.All[Document]())
}
