package kernel

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

type Account struct {
	_ struct{} `golem:"model;id=example.kernel.Account;table=accounts"`
	_ struct{} `golem:"unique=uq_accounts_handle(handle)"`

	ID     golem.UUID `db:"id" golem:"id=example.kernel.Account.ID;pk;default=uuid"`
	Handle string     `db:"handle" golem:"type=varchar(40)"`
	Notes  []Note     `db:"-" golem:"relation=has_many;fields=id;references=owner_id"`
}

type Note struct {
	_ struct{} `golem:"model;id=example.kernel.Note;table=notes"`
	_ struct{} `golem:"index=idx_notes_owner(owner_id,id)"`

	ID       golem.UUID `db:"id" golem:"id=example.kernel.Note.ID;pk;default=uuid"`
	OwnerID  golem.UUID `db:"owner_id" golem:"immutable"`
	Title    string     `db:"title" golem:"type=varchar(160)"`
	Archived bool       `db:"archived" golem:"default=false"`
	Owner    *Account   `db:"-" golem:"relation=belongs_to;fields=owner_id;references=id"`
}

func (Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(
		golem.GraphQL[Note](golem.GraphQLHookOwned(Notes.OwnerID)),
		golem.Subscriptions[Note](),
		golem.RelationOptions(Notes.Owner).OnDelete(golem.Cascade),
	)
}

func (Note) BeforeCreate(ctx context.Context, request *NoteCreateRequest) error {
	return golem.SetCreate(request, Notes.OwnerID, golem.ActorFrom[Actor](ctx).AccountID)
}

func (Account) DefinePolicy(rules *golem.Rules[Account], actor Actor) {
	rules.CanRead(Accounts.ID.Eq(actor.AccountID))
}

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	owned := Notes.OwnerID.Eq(actor.AccountID)
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanDelete(owned)
	golem.Within(rules, ArchiveNotes).CanUpdateFields(owned, Notes.Archived)
}

type ArchiveNotesArgs struct {
	Title string `golem:"graphql=title"`
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "archiveNotes", ArchiveNotes)
}

func ArchiveNotes(ctx context.Context, caller *Caller[Principal], arguments ArchiveNotesArgs) (int64, error) {
	return caller.Notes.UpdateMany(ctx, Notes.Title.Eq(arguments.Title), Notes.UpdateMany(Notes.Archived.Set(true)))
}
