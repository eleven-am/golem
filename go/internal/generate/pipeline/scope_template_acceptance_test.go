package pipeline

import (
	"os"
	"strings"
	"testing"
)

// Exercise the emitted namespace, frozen provenance and registry binder through
// real generated callers, authorized reads/counts/masks and final write checks.
func TestFreshGeneratedScopeTemplatePreservesActorDomain(t *testing.T) {
	runGeneratedScopeTemplateConsumer(t, false)
}

func TestFreshGeneratedFactTemplatePreservesActorDomain(t *testing.T) {
	runGeneratedScopeTemplateConsumer(t, true)
}

func runGeneratedScopeTemplateConsumer(t *testing.T, boundFact bool) {
	t.Helper()
	if os.Getenv("GOLEM_POLICY_HISTORY_WRITER_REHEARSAL") != "4" {
		t.Skip("run scripts/verify.sh writer4-database for historical policy facts")
	}
	consumer := strings.Replace(scalarSetsConsumerTest, `"github.com/eleven-am/golem/go/golem"`, `"github.com/eleven-am/golem/go/golem"
 "github.com/eleven-am/golem/go/events"`, 1)
	consumer = strings.Replace(consumer, "application, err := app.Open", `eventTransport, err := events.NewMemoryTransport(events.MemoryLimits{})
 if err != nil { t.Fatal(err) }
 application, err := app.Open`, 1)
	consumer = strings.Replace(consumer, "Database: database,", `Database: database,
  EventTransport: eventTransport,
  ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},`, 1)
	runGeneratedDTOConsumer(t, "scalarsets", func(source string) string {
		const inline = "scope := Items.Owner.In(actor.Allowed...)"
		if strings.Count(source, inline) != 1 {
			t.Fatal("scope fixture no longer has its expected policy declaration")
		}
		definition := `scope := rules.Fact("in-domain", golem.All[Item]())`
		factDeclaration := ""
		if boundFact {
			definition = `scope := rules.BindFact(ItemFacts.InDomain(ItemScopeArgs{OwnerIDs: actor.Allowed}))`
			factDeclaration = `golem.FactTemplate("inDomain", "allowed", func(args ItemScopeArgs) golem.Predicate[Item] {
   return Items.Owner.In(args.OwnerIDs...)
  }),`
		}
		source = strings.Replace(source, inline, "rules.BindFactScope(ItemScopes.Allowed(ItemScopeArgs{OwnerIDs: actor.Allowed}))\n\t"+definition, 1)
		return source + `
type ItemScopeArgs struct { OwnerIDs []golem.UUID }

func (Item) GolemModel() golem.ModelSpec[Item] {
 return golem.DefineModel(golem.Subscriptions(
  golem.ScopeTemplate("allowed", func(args ItemScopeArgs) golem.Predicate[Item] {
   return Items.Owner.In(args.OwnerIDs...)
  }),` + factDeclaration + `
 ))
}
`
	}, consumer)
}
