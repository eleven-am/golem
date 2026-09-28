package semanticbenchmark

import "github.com/eleven-am/golem/go/golem"

type Principal struct{}

type Actor struct {
	Readable bool
}

type Document struct {
	_       struct{} `golem:"model;id=semantic.BenchmarkDocument;table=semantic_benchmark_documents"`
	ID      string   `db:"id" golem:"id=semantic.BenchmarkDocument.ID;pk"`
	Mailbox int64    `db:"mailbox"`
	Body    string   `db:"body"`
}

func (Document) GolemModel() golem.ModelSpec[Document] {
	return golem.DefineModel(
		golem.Index[Document]("idx_semantic_benchmark_mailbox").Keys(golem.IndexColumn(Documents.Mailbox)),
		golem.SemanticIndex("content", "benchmark", Documents.Body),
	)
}

func (Document) DefinePolicy(rules *golem.Rules[Document], actor Actor) {
	if actor.Readable {
		rules.CanRead(golem.All[Document]())
	}
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "semantic_benchmark")
	golem.Actor[Actor](schema)
	golem.Model[Document](schema)
	golem.Providers(schema, golem.SQLite)
	golem.EmbeddingSpace(schema, "benchmark", 1024)
}
