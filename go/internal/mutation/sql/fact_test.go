package sql

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type factPost struct{}
type factUser struct{}

func TestSQLiteFactFinalVerificationSeesSameTransactionRevocation(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[factPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	author := golem.GeneratedToOne[factPost, factUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	name := golem.GeneratedTextField[factUser, string](fixture.UserName)
	title := golem.GeneratedTextField[factPost, string](fixture.PostTitle)
	root := policyir.ModelID(fixture.Post)
	id, authorID := golem.UUID{15: 3}, golem.UUID{15: 4}
	for _, mode := range []struct{ negative, relatedScope bool }{{false, false}, {true, false}, {false, true}} {
		negative := mode.negative
		predicate := author.Is(name.Eq("enabled"))
		if negative {
			predicate = author.Is(name.Eq("disabled")).Not()
		}
		frozen, err := predicate.Freeze(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		body, err := policybind.Predicate(frozen, fixture.Registry, policyir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		scopePredicate, err := title.Eq("excluded").Not().Freeze(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		scopeCondition, err := policybind.Predicate(scopePredicate, fixture.Registry, policyir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		if mode.relatedScope {
			// Isolate the authority gate: the named data fact is always true.
			// Only the unreadable related User can revoke this write.
			scopeCondition = body
			body, _ = policyir.NewConstant(root, true)
		}
		scope, err := policyir.NewFactScope(scopeCondition)
		if err != nil {
			t.Fatal(err)
		}
		definition, _ := policyir.NewFactDefinition(root, "author_enabled", body)
		reference, _ := policyir.NewFactReference(root, definition.ID())
		rule, _ := policyir.NewModelRule(policyir.ActionUpdate, policyir.EffectGrant, root, &reference, 0)
		policy, err := policyir.NewPolicyWithFacts(root, []policyir.Rule{rule}, []policyir.FactDefinition{definition}, scope)
		if err != nil {
			t.Fatal(err)
		}
		fact, err := resolve.RowConstraint(policy, policyir.ActionUpdate, root)
		if err != nil {
			t.Fatal(err)
		}
		selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, fact)
		target := target(t, fixture, policyir.UUIDValue(id.Bytes()), nil)
		image, _ := mutationir.NewImageRequirements(root, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle)}, nil)
		graph, err := mutationir.NewGraph(mutationir.NodeInput{
			Operation: mutationir.Update, Model: root, Target: &target,
			ScalarOperations: []mutationir.ScalarOperation{setString(t, fixture, fixture.PostTitle, "changed")},
			Before:           image, After: image, Selection: &selection, RowPostcondition: &fact, Identity: mutationir.IdentityUnchanged,
		})
		if err != nil {
			t.Fatal(err)
		}
		planned := plan(t, graph, image)
		for _, revokeMode := range []string{"none", "dependency", "scope"} {
			if mode.relatedScope && revokeMode == "dependency" {
				continue
			}
			t.Run(fmt.Sprintf("negative=%v/related-scope=%v/revoke=%s", negative, mode.relatedScope, revokeMode), func(t *testing.T) {
				revoke := revokeMode != "none"
				ctx := context.Background()
				provider := sqliteprovider.New()
				database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "fact-write.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = database.Close() })
				if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`INSERT INTO users(id,name) VALUES(?,?)`, authorID.String(), "enabled"); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`INSERT INTO posts(id,author_id,title) VALUES(?,?,?)`, id.String(), authorID.String(), "original"); err != nil {
					t.Fatal(err)
				}
				proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
				if err != nil {
					t.Fatal(err)
				}
				program := mustRender(t, planned, fixture, proof)
				tx, err := database.BeginTxx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				results := map[uint32]map[policyir.FieldID]any{}
				refused, verified := false, false
				for step, statement := range program.Statements() {
					arguments := make([]any, len(statement.Bindings()))
					for index, binding := range statement.Bindings() {
						if value, static := binding.StaticValue(); static {
							arguments[index] = value
						} else {
							source, field, ok := binding.PriorResult()
							if !ok {
								t.Fatal("unknown statement binding")
							}
							arguments[index] = results[source][field]
						}
					}
					rows, err := tx.QueryxContext(ctx, statement.SQL(), arguments...)
					if err != nil {
						t.Fatal(err)
					}
					if !rows.Next() {
						streamErr := rows.Err()
						_ = rows.Close()
						if streamErr != nil || !revoke || statement.Role() != VerifyPostcondition {
							t.Fatalf("unexpected empty statement role=%d err=%v", statement.Role(), streamErr)
						}
						refused = true
						break
					}
					values := map[string]any{}
					if err := rows.MapScan(values); err != nil {
						_ = rows.Close()
						t.Fatal(err)
					}
					if rows.Next() || rows.Err() != nil {
						_ = rows.Close()
						t.Fatal("statement did not return exactly one row")
					}
					if err := rows.Close(); err != nil {
						t.Fatal(err)
					}
					result := map[policyir.FieldID]any{}
					for _, column := range statement.Columns() {
						result[column.FieldID()] = values[column.Alias()]
					}
					results[uint32(step)] = result
					verified = verified || statement.Role() == VerifyPostcondition
					if revoke && statement.Role() == ApplyUpdate {
						if revokeMode == "scope" && !mode.relatedScope {
							_, err = tx.Exec(`UPDATE posts SET title='excluded' WHERE id=?`, id.String())
						} else {
							_, err = tx.Exec(`UPDATE users SET name='disabled' WHERE id=?`, authorID.String())
						}
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				if refused != revoke || !refused && !verified {
					t.Fatal("final fact verification was skipped or did not see the changed dependency")
				}
				wantTitle := "changed"
				if refused {
					wantTitle = "original"
					err = tx.Rollback()
				} else {
					err = tx.Commit()
				}
				if err != nil {
					t.Fatal(err)
				}
				var title, storedName string
				if err := database.Get(&title, `SELECT title FROM posts WHERE id=?`, id.String()); err != nil || title != wantTitle {
					t.Fatalf("title=%q want=%q err=%v", title, wantTitle, err)
				}
				if err := database.Get(&storedName, `SELECT name FROM users WHERE id=?`, authorID.String()); err != nil || storedName != "enabled" {
					t.Fatalf("dependency change escaped rollback: name=%q err=%v", storedName, err)
				}
			})
		}
	}
}
