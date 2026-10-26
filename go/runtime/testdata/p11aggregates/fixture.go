package p11aggregates

import (
	"context"
	"sync"

	golem "github.com/eleven-am/golem/go/golem"
)

type Principal struct {
	ID            string
	Misconfigured bool
	Unregistered  bool
}

type Actor struct {
	ID            string
	Misconfigured bool
	Unregistered  bool
}

type Artifact struct {
	_ struct{} `golem:"model;id=p11aggregates.Artifact;table=artifacts"`

	ID          golem.UUID   `db:"id" golem:"id=p11aggregates.Artifact.ID;pk"`
	Owner       string       `db:"owner" golem:"type=varchar(80)"`
	Manager     string       `db:"manager" golem:"type=varchar(80)"`
	Title       string       `db:"title" golem:"type=varchar(80)"`
	Collections []Collection `db:"-" golem:"relation=has_many;fields=id;references=artifact_id"`
	Assets      []Asset      `db:"-" golem:"relation=has_many;fields=id;references=artifact_id"`
	Shares      []Share      `db:"-" golem:"relation=has_many;fields=id;references=artifact_id"`
	Invites     []Invite     `db:"-" golem:"relation=has_many;fields=id;references=artifact_id"`
}

type Collection struct {
	_ struct{} `golem:"model;id=p11aggregates.Collection;table=collections"`

	ID         golem.UUID `db:"id" golem:"id=p11aggregates.Collection.ID;pk"`
	ArtifactID golem.UUID `db:"artifact_id"`
	Owner      string     `db:"owner" golem:"type=varchar(80)"`
	Name       string     `db:"name" golem:"type=varchar(80)"`
	Artifact   *Artifact  `db:"-" golem:"relation=belongs_to;fields=artifact_id;references=id"`
	Documents  []Document `db:"-" golem:"relation=has_many;fields=id;references=collection_id"`
}

type Document struct {
	_ struct{} `golem:"model;id=p11aggregates.Document;table=documents"`

	ID           golem.UUID  `db:"id" golem:"id=p11aggregates.Document.ID;pk"`
	CollectionID golem.UUID  `db:"collection_id"`
	Owner        string      `db:"owner" golem:"type=varchar(80)"`
	Title        string      `db:"title" golem:"type=varchar(80)"`
	Collection   *Collection `db:"-" golem:"relation=belongs_to;fields=collection_id;references=id"`
}

type Asset struct {
	_ struct{} `golem:"model;id=p11aggregates.Asset;table=assets"`

	ID         golem.UUID `db:"id" golem:"id=p11aggregates.Asset.ID;pk"`
	ArtifactID golem.UUID `db:"artifact_id"`
	Owner      string     `db:"owner" golem:"type=varchar(80)"`
	Label      string     `db:"label" golem:"type=varchar(80)"`
	Size       int64      `db:"size"`
	Artifact   *Artifact  `db:"-" golem:"relation=belongs_to;fields=artifact_id;references=id"`
}

type Share struct {
	_ struct{} `golem:"model;id=p11aggregates.Share;table=shares"`

	ID         golem.UUID `db:"id" golem:"id=p11aggregates.Share.ID;pk"`
	ArtifactID golem.UUID `db:"artifact_id"`
	Grantee    string     `db:"grantee" golem:"type=varchar(80)"`
	Artifact   *Artifact  `db:"-" golem:"relation=belongs_to;fields=artifact_id;references=id"`
}

type Invite struct {
	_ struct{} `golem:"model;id=p11aggregates.Invite;table=invites"`

	ID         golem.UUID `db:"id" golem:"id=p11aggregates.Invite.ID;pk"`
	ArtifactID golem.UUID `db:"artifact_id"`
	Owner      string     `db:"owner" golem:"type=varchar(80)"`
	Email      string     `db:"email" golem:"type=varchar(80)"`
	Status     string     `db:"status" golem:"type=varchar(40)"`
	Artifact   *Artifact  `db:"-" golem:"relation=belongs_to;fields=artifact_id;references=id"`
}

type DocumentChange struct {
	_ struct{} `golem:"model;id=p11aggregates.DocumentChange;table=document_changes"`

	ID         golem.UUID `db:"id" golem:"id=p11aggregates.DocumentChange.ID;pk"`
	DocumentID golem.UUID `db:"document_id"`
	Author     string     `db:"author" golem:"type=varchar(80)"`
	Note       string     `db:"note" golem:"type=varchar(80)"`
}

func (Asset) GolemModel() golem.ModelSpec[Asset] {
	return golem.DefineModel(golem.Subscriptions[Asset]())
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "p11_aggregates")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[Artifact](schema)
	golem.Model[Collection](schema)
	golem.Model[Document](schema)
	golem.Model[Asset](schema)
	golem.Model[Share](schema)
	golem.Model[Invite](schema)
	golem.Model[DocumentChange](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (Artifact) DefinePolicy(rules *golem.Rules[Artifact], actor Actor) {
	rules.CanRead(golem.All[Artifact]())
	if actor.Misconfigured {
		golem.Within(rules, RenameArtifact).CanCount(Artifacts.Owner.Eq(actor.ID))
	}
	if actor.Unregistered {
		golem.Within(rules, NotAnOperation).CanCount(Artifacts.Owner.Eq(actor.ID))
	}
}

func (Collection) DefinePolicy(rules *golem.Rules[Collection], actor Actor) {
	rules.CanRead(Collections.Owner.Eq(actor.ID))
	golem.Within(rules, CheckDocumentCapacity).CanCount(Collections.Artifact.Is(Artifacts.Manager.Eq(actor.ID)))
}

func (Document) DefinePolicy(rules *golem.Rules[Document], actor Actor) {
	rules.CanRead(Documents.Owner.Eq(actor.ID))
	managed := Documents.Collection.Is(Collections.Artifact.Is(Artifacts.Manager.Eq(actor.ID)))
	golem.Within(rules, CheckDocumentCapacity).CanCount(managed)
	golem.Within(rules, CountManagedDocuments).CanCount(managed)
}

func (Asset) DefinePolicy(rules *golem.Rules[Asset], actor Actor) {
	rules.CanRead(Assets.Owner.Eq(actor.ID))
	golem.Within(rules, CommitAssetBudget).CanAggregate(Assets.Artifact.Is(Artifacts.Owner.Eq(actor.ID)), Assets.Size, Assets.ArtifactID)
}

func (Share) DefinePolicy(rules *golem.Rules[Share], actor Actor) {
	rules.CanRead(Shares.Grantee.Eq(actor.ID))
	golem.Within(rules, CheckShareCapacity).CanCount(Shares.Artifact.Is(Artifacts.Owner.Eq(actor.ID)))
}

func (Invite) DefinePolicy(rules *golem.Rules[Invite], actor Actor) {
	rules.CanRead(Invites.Email.Eq(actor.ID))
	golem.Within(rules, CheckShareCapacity).CanCount(Invites.Artifact.Is(Artifacts.Owner.Eq(actor.ID)))
	golem.Within(rules, CountPendingInvites).CanCount(Invites.Owner.Eq(actor.ID).And(Invites.Status.Eq("pending")))
}

func (DocumentChange) DefinePolicy(rules *golem.Rules[DocumentChange], actor Actor) {
	rules.CanRead(DocumentChanges.Author.Eq(actor.ID))
	golem.Within(rules, RecordDocumentChange).CanCreate(DocumentChanges.Author.Eq(actor.ID))
}

type RenameArgs struct {
	ID    golem.UUID `golem:"graphql=id"`
	Title string     `golem:"graphql=title"`
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "renameArtifact", RenameArtifact)
}

func RenameArtifact(ctx context.Context, caller *Caller[Principal], arguments RenameArgs) (int64, error) {
	return caller.Artifacts.Count(ctx)
}

func NotAnOperation(context.Context, *CallerTx[Principal], struct{}) (int64, error) {
	return 0, nil
}

type BudgetArgs struct {
	ArtifactID golem.UUID
}

type ChangeArgs struct {
	ID         golem.UUID
	DocumentID golem.UUID
	Author     string
	Note       string
}

func DefineOperations(operations *golem.Operations) {
	golem.TxOperation(operations, "commitAssetBudget", CommitAssetBudget)
	golem.TxOperation(operations, "checkDocumentCapacity", CheckDocumentCapacity)
	golem.TxOperation(operations, "countManagedDocuments", CountManagedDocuments)
	golem.TxOperation(operations, "checkShareCapacity", CheckShareCapacity)
	golem.TxOperation(operations, "countPendingInvites", CountPendingInvites)
	golem.TxOperation(operations, "recordDocumentChange", RecordDocumentChange)
}

func CommitAssetBudget(ctx context.Context, tx *CallerTx[Principal], arguments BudgetArgs) (int64, error) {
	if err := runProbe(ctx, "commitAssetBudget", tx); err != nil {
		return 0, err
	}
	total := Assets.Size.Sum()
	result, err := tx.Assets.Aggregate(ctx, Assets.Aggregate(Assets.AggregateSelect(total), Assets.AggregateWhere(Assets.ArtifactID.Eq(arguments.ArtifactID))))
	if err != nil {
		return 0, err
	}
	sum, present := golem.AggregateValue(result, total).Get()
	if !present {
		return 0, nil
	}
	value, _ := sum.Int64()
	return value, nil
}

func CheckDocumentCapacity(ctx context.Context, tx *CallerTx[Principal], _ struct{}) (int64, error) {
	if err := runProbe(ctx, "checkDocumentCapacity", tx); err != nil {
		return 0, err
	}
	return tx.Documents.Count(ctx)
}

func CountManagedDocuments(ctx context.Context, tx *CallerTx[Principal], _ struct{}) (int64, error) {
	return tx.Documents.Count(ctx)
}

func CheckShareCapacity(ctx context.Context, tx *CallerTx[Principal], _ struct{}) (int64, error) {
	shares, err := tx.Shares.Count(ctx)
	if err != nil {
		return 0, err
	}
	invites, err := tx.Invites.Count(ctx)
	if err != nil {
		return 0, err
	}
	return shares + invites, nil
}

func CountPendingInvites(ctx context.Context, tx *CallerTx[Principal], _ struct{}) (int64, error) {
	if err := runProbe(ctx, "countPendingInvites", tx); err != nil {
		return 0, err
	}
	return tx.Invites.Count(ctx)
}

func RecordDocumentChange(ctx context.Context, tx *CallerTx[Principal], arguments ChangeArgs) (string, error) {
	created, err := tx.DocumentChanges.Create(ctx, DocumentChanges.Create(
		DocumentChanges.ID.Create(arguments.ID),
		DocumentChanges.DocumentID.Create(arguments.DocumentID),
		DocumentChanges.Author.Create(arguments.Author),
		DocumentChanges.Note.Create(arguments.Note),
	), DocumentChanges.Select(DocumentChanges.ID))
	if err != nil {
		return "", err
	}
	id, _ := golem.Value(created, DocumentChanges.ID).Get()
	return id.String(), nil
}

type Probe func(ctx context.Context, operation string, tx *CallerTx[Principal]) error

var (
	probeLock   sync.Mutex
	activeProbe Probe
)

func SetProbe(probe Probe) {
	probeLock.Lock()
	defer probeLock.Unlock()
	activeProbe = probe
}

func runProbe(ctx context.Context, operation string, tx *CallerTx[Principal]) error {
	probeLock.Lock()
	probe := activeProbe
	probeLock.Unlock()
	if probe == nil {
		return nil
	}
	return probe(ctx, operation, tx)
}
