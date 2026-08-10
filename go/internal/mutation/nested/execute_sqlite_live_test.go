package nested

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	_ "modernc.org/sqlite"
)

func TestSQLiteSourceConnectOrCreatePersistsBranchResultAndRollsBackDenial(t *testing.T) {
	for _, test := range []struct {
		name        string
		seedTarget  bool
		deny        bool
		wantAuthor  byte
		wantUsers   int
		wantTouched uint32
	}{
		{name: "connect", seedTarget: true, wantAuthor: 2, wantUsers: 1, wantTouched: 2},
		{name: "create", wantAuthor: 3, wantUsers: 1, wantTouched: 3},
		{name: "deny", deny: true, wantAuthor: 1, wantUsers: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := schematest.New(t)
			database, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatal(err)
			}
			database.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = database.Close() })
			if _, err := database.Exec("CREATE TABLE users (id BLOB PRIMARY KEY, name TEXT NOT NULL); CREATE TABLE posts (id BLOB PRIMARY KEY, author_id BLOB NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			postID, original := [16]byte{9}, [16]byte{15: 1}
			if _, err := database.Exec("INSERT INTO posts(id, author_id) VALUES (?, ?)", postID[:], original[:]); err != nil {
				t.Fatal(err)
			}
			targetID, createdID := [16]byte{15: 2}, [16]byte{15: 3}
			if test.seedTarget {
				if _, err := database.Exec("INSERT INTO users(id, name) VALUES (?, ?)", targetID[:], "existing"); err != nil {
					t.Fatal(err)
				}
			}
			target := golem.GeneratedUniqueSelectorValue[nestedUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, golem.NewUUID(targetID)))
			create := golem.GeneratedCreateInput[nestedUser](fixture.User,
				golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedEqualField[nestedUser, golem.UUID](fixture.UserID), golem.NewUUID(createdID)),
				golem.GeneratedCreateFieldValue(fixture.User, golem.GeneratedTextField[nestedUser, string](fixture.UserName), "created"),
			)
			relations := freezeRelations(t, golem.GeneratedUpdateInput[nestedPost](fixture.Post,
				golem.GeneratedNestedConnectOrCreate[nestedPost, nestedUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, target, create),
			))
			built, err := Build(Request{Root: rootForModel(t, fixture, fixture.Post, fixture.PostKey, fixture.PostID), Mutations: relations, Stance: mutationir.System, Registry: fixture.Registry, MaxDepth: 3, MaxRows: 5})
			if err != nil {
				t.Fatal(err)
			}
			boundary := sqliteCOCBoundary{database: database, registry: fixture.Registry, fixture: fixture, deny: test.deny}
			receipt, executeErr := Execute(context.Background(), built.Graph(), 8, boundary)
			if test.deny {
				if !errors.Is(executeErr, errSQLiteCOCDenied) {
					t.Fatalf("denial error=%v", executeErr)
				}
			} else if executeErr != nil {
				t.Fatal(executeErr)
			} else if receipt.TouchedRows() != test.wantTouched {
				t.Fatalf("touched=%d want %d", receipt.TouchedRows(), test.wantTouched)
			}
			var author []byte
			if err := database.QueryRow("SELECT author_id FROM posts WHERE id = ?", postID[:]).Scan(&author); err != nil {
				t.Fatal(err)
			}
			if len(author) != 16 || author[15] != test.wantAuthor {
				t.Fatalf("author=%x want suffix %d", author, test.wantAuthor)
			}
			var users int
			if err := database.QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil {
				t.Fatal(err)
			}
			if users != test.wantUsers {
				t.Fatalf("users=%d want %d", users, test.wantUsers)
			}
		})
	}
}

var errSQLiteCOCDenied = errors.New("sqlite connect-or-create denied")

type sqliteCOCBoundary struct {
	database *sql.DB
	registry *schema.Registry
	fixture  schematest.Fixture
	deny     bool
}

func (boundary sqliteCOCBoundary) BeginNested(ctx context.Context) (ExecutionTransaction, error) {
	tx, err := boundary.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqliteCOCTransaction{tx: tx, registry: boundary.registry, fixture: boundary.fixture, deny: boundary.deny}, nil
}

type sqliteCOCTransaction struct {
	tx       *sql.Tx
	registry *schema.Registry
	fixture  schematest.Fixture
	deny     bool
}

func (transaction *sqliteCOCTransaction) ExpandNested(ctx context.Context, request ExpansionRequest) (RuntimeExpansion, error) {
	node := request.Node()
	if node.Operation() == mutationir.ConnectOrCreate {
		position, _ := node.RelationPosition()
		target, _ := position.Target()
		id, err := targetUUID(target, policyir.FieldID(transaction.fixture.UserID))
		if err != nil {
			return RuntimeExpansion{}, err
		}
		var count int
		if err := transaction.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id = ?", id[:]).Scan(&count); err != nil {
			return RuntimeExpansion{}, err
		}
		branch := mutationir.ConnectOrCreateCreateBranch
		if count == 1 {
			branch = mutationir.ConnectOrCreateConnectBranch
		}
		return NewRuntimeExpansion(nil, branch)
	}
	if node.Operation() == mutationir.Create {
		work, _ := NewCreateWork(node.ModelID(), []byte{1})
		return NewRuntimeExpansion([]RuntimeWork{work}, 0)
	}
	var row mutationdecode.Row
	var err error
	if node.Operation() == mutationir.BranchProbe {
		position, _ := node.RelationPosition()
		target, _ := position.Target()
		id, targetErr := targetUUID(target, policyir.FieldID(transaction.fixture.UserID))
		if targetErr != nil {
			return RuntimeExpansion{}, targetErr
		}
		row, err = transaction.user(ctx, id)
	} else if anchor, ok := request.RelationAnchor(); ok {
		row, _ = anchor.Result().After()
	} else {
		target, ok := node.Target()
		if !ok {
			return RuntimeExpansion{}, fmt.Errorf("root target is absent")
		}
		id, targetErr := targetUUID(target, policyir.FieldID(transaction.fixture.PostID))
		if targetErr != nil {
			return RuntimeExpansion{}, targetErr
		}
		row, err = transaction.post(ctx, id)
		if err != nil {
			return RuntimeExpansion{}, err
		}
	}
	identity, err := mutationdecode.PrimaryIdentity(transaction.registry, row)
	if err != nil {
		return RuntimeExpansion{}, err
	}
	work, err := NewExistingWork(node.ModelID(), identity, []byte{1})
	if err != nil {
		return RuntimeExpansion{}, err
	}
	return NewRuntimeExpansion([]RuntimeWork{work}, 0)
}

func (transaction *sqliteCOCTransaction) ApplyNested(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	node := request.Node()
	switch node.Operation() {
	case mutationir.Update:
		identity, _ := request.Work().Identity()
		id, err := identityUUID(identity, policyir.FieldID(transaction.fixture.PostID))
		if err != nil {
			return ApplyResult{}, err
		}
		row, err := transaction.post(ctx, id)
		if err != nil {
			return ApplyResult{}, err
		}
		return NewApplyResult(&row, &row), nil
	case mutationir.Create:
		var id [16]byte
		var name string
		for _, operation := range node.ScalarOperations() {
			value, _ := operation.Value()
			if operation.FieldID() == policyir.FieldID(transaction.fixture.UserID) {
				id, _ = value.UUID()
			}
			if operation.FieldID() == policyir.FieldID(transaction.fixture.UserName) {
				name, _ = value.Text()
			}
		}
		if _, err := transaction.tx.ExecContext(ctx, "INSERT INTO users(id, name) VALUES (?, ?)", id[:], name); err != nil {
			return ApplyResult{}, err
		}
		row := userRow(transaction.registry, transaction.fixture, id, name)
		return NewApplyResult(nil, &row), nil
	case mutationir.BranchProbe:
		identity, _ := request.Work().Identity()
		id, err := identityUUID(identity, policyir.FieldID(transaction.fixture.UserID))
		if err != nil {
			return ApplyResult{}, err
		}
		row, err := transaction.user(ctx, id)
		if err != nil {
			return ApplyResult{}, err
		}
		return NewApplyResult(nil, &row), nil
	case mutationir.Connect:
		parent, parentOK := request.Parent()
		anchor, anchorOK := request.RelationAnchor()
		if !parentOK || !anchorOK {
			return ApplyResult{}, fmt.Errorf("conditional owner effect lacks branch result or anchor")
		}
		userAfter, _ := parent.Result().After()
		postBefore, _ := anchor.Result().After()
		userIdentity, _ := mutationdecode.PrimaryIdentity(transaction.registry, userAfter)
		postIdentity, _ := mutationdecode.PrimaryIdentity(transaction.registry, postBefore)
		userID, err := identityUUID(userIdentity, policyir.FieldID(transaction.fixture.UserID))
		if err != nil {
			return ApplyResult{}, err
		}
		postID, err := identityUUID(postIdentity, policyir.FieldID(transaction.fixture.PostID))
		if err != nil {
			return ApplyResult{}, err
		}
		before, err := transaction.post(ctx, postID)
		if err != nil {
			return ApplyResult{}, err
		}
		if _, err := transaction.tx.ExecContext(ctx, "UPDATE posts SET author_id = ? WHERE id = ?", userID[:], postID[:]); err != nil {
			return ApplyResult{}, err
		}
		after, err := transaction.post(ctx, postID)
		if err != nil {
			return ApplyResult{}, err
		}
		return NewApplyResult(&before, &after), nil
	default:
		return ApplyResult{}, fmt.Errorf("unexpected live operation %d", node.Operation())
	}
}

func (transaction *sqliteCOCTransaction) VerifyNested(_ context.Context, applied AppliedNode) error {
	if transaction.deny && applied.Node().Operation() == mutationir.Connect {
		return errSQLiteCOCDenied
	}
	return nil
}
func (transaction *sqliteCOCTransaction) CommitNested(context.Context) error {
	return transaction.tx.Commit()
}
func (transaction *sqliteCOCTransaction) RollbackNested(context.Context) error {
	return transaction.tx.Rollback()
}

func (transaction *sqliteCOCTransaction) post(ctx context.Context, id [16]byte) (mutationdecode.Row, error) {
	var storedID, author []byte
	if err := transaction.tx.QueryRowContext(ctx, "SELECT id, author_id FROM posts WHERE id = ?", id[:]).Scan(&storedID, &author); err != nil {
		return mutationdecode.Row{}, err
	}
	var postID, authorID [16]byte
	copy(postID[:], storedID)
	copy(authorID[:], author)
	return mutationdecode.NewRow(transaction.registry, policyir.ModelID(transaction.fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(transaction.fixture.PostID), policyir.UUIDValue(postID)),
		mutationdecode.Value(policyir.FieldID(transaction.fixture.AuthorID), policyir.UUIDValue(authorID)),
	})
}

func (transaction *sqliteCOCTransaction) user(ctx context.Context, id [16]byte) (mutationdecode.Row, error) {
	var storedID []byte
	var name string
	if err := transaction.tx.QueryRowContext(ctx, "SELECT id, name FROM users WHERE id = ?", id[:]).Scan(&storedID, &name); err != nil {
		return mutationdecode.Row{}, err
	}
	var userID [16]byte
	copy(userID[:], storedID)
	return userRow(transaction.registry, transaction.fixture, userID, name), nil
}

func userRow(registry *schema.Registry, fixture schematest.Fixture, id [16]byte, name string) mutationdecode.Row {
	text, _ := policyir.StringValue(name)
	row, err := mutationdecode.NewRow(registry, policyir.ModelID(fixture.User), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.UserID), policyir.UUIDValue(id)),
		mutationdecode.Value(policyir.FieldID(fixture.UserName), text),
	})
	if err != nil {
		panic(err)
	}
	return row
}

func targetUUID(target mutationir.Target, field policyir.FieldID) ([16]byte, error) {
	for _, value := range target.Values() {
		if value.FieldID() == field {
			if id, ok := value.Value().UUID(); ok {
				return id, nil
			}
		}
	}
	return [16]byte{}, fmt.Errorf("UUID target component is absent")
}

func identityUUID(identity mutationdecode.Identity, field policyir.FieldID) ([16]byte, error) {
	for _, component := range identity.Components() {
		if component.FieldID() == field {
			value, ok := component.PolicyValue()
			if ok {
				if id, uuid := value.UUID(); uuid {
					return id, nil
				}
			}
		}
	}
	return [16]byte{}, fmt.Errorf("UUID identity component is absent")
}
