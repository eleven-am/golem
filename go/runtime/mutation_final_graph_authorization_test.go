package runtime

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCallerCreateRowAuthorizationUsesCompletedInverseRelationGraph(t *testing.T) {
	for _, relationAction := range []string{"create", "connect"} {
		relationAction := relationAction
		t.Run(relationAction+"-final-pass", func(t *testing.T) {
			forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
				posts := golem.GeneratedToMany[graphMutationUser, graphMutationPost](fixture.schema.UserPosts, fixture.schema.Authorship)
				fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
					func(rules *golem.Rules[graphMutationUser]) {
						rules.CanCreate(posts.Some(fixture.postTitle.Eq("allowed")))
					},
					func(rules *golem.Rules[graphMutationPost]) {
						rules.CanCreate(golem.All[graphMutationPost]())
						rules.CanUpdate(golem.All[graphMutationPost]())
					},
					nil,
				), nil)
				assertFinalGraphUserCreate(t, fixture, relationAction, "allowed", true)
			})
		})
		t.Run(relationAction+"-transient-pass-final-fail", func(t *testing.T) {
			forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
				posts := golem.GeneratedToMany[graphMutationUser, graphMutationPost](fixture.schema.UserPosts, fixture.schema.Authorship)
				fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
					func(rules *golem.Rules[graphMutationUser]) {
						rules.CanCreate(posts.None(fixture.postTitle.Eq("blocked")))
					},
					func(rules *golem.Rules[graphMutationPost]) {
						rules.CanCreate(golem.All[graphMutationPost]())
						rules.CanUpdate(golem.All[graphMutationPost]())
					},
					nil,
				), nil)
				assertFinalGraphUserCreate(t, fixture, relationAction, "blocked", false)
			})
		})
	}
}

func TestCallerCreateAuthoredFieldAuthorizationUsesCompletedRelationGraph(t *testing.T) {
	for _, test := range []struct {
		name, title string
		allowed     bool
	}{{name: "final-pass", title: "allowed", allowed: true}, {name: "final-fail", title: "blocked", allowed: false}} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
				posts := golem.GeneratedToMany[graphMutationUser, graphMutationPost](fixture.schema.UserPosts, fixture.schema.Authorship)
				fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
					func(rules *golem.Rules[graphMutationUser]) {
						rules.CanCreate(golem.All[graphMutationUser]())
						rules.CannotCreateFields(golem.All[graphMutationUser](), fixture.userName)
						rules.CanCreateFields(posts.Some(fixture.postTitle.Eq("allowed")), fixture.userName)
					},
					func(rules *golem.Rules[graphMutationPost]) { rules.CanCreate(golem.All[graphMutationPost]()) },
					nil,
				), nil)
				assertFinalGraphUserCreate(t, fixture, "create", test.title, test.allowed)
			})
		})
	}
}

func TestCallerUpdateAuthorizationUsesCompletedRelationGraphAndFieldPreimage(t *testing.T) {
	for _, test := range []struct {
		name, replacement string
		allowed           bool
	}{{name: "temporarily-false-final-pass", replacement: "new", allowed: true}, {name: "true-before-final-fail", replacement: "blocked", allowed: false}} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
				comments := golem.GeneratedToMany[graphMutationPost, graphMutationComment](fixture.schema.PostComments, fixture.schema.Commenting)
				oldReach := comments.Some(fixture.commentBody.Eq("old"))
				finalReach := oldReach.Or(comments.Some(fixture.commentBody.Eq("new")))
				fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
					func(rules *golem.Rules[graphMutationUser]) { rules.CanCreate(golem.All[graphMutationUser]()) },
					func(rules *golem.Rules[graphMutationPost]) {
						rules.CanCreate(golem.All[graphMutationPost]())
						rules.CanUpdate(finalReach)
						rules.CannotUpdateFields(golem.All[graphMutationPost](), fixture.postTitle)
						rules.CanUpdateFields(oldReach, fixture.postTitle)
					},
					func(rules *golem.Rules[graphMutationComment]) {
						rules.CanCreate(golem.All[graphMutationComment]())
						rules.CanDelete(golem.All[graphMutationComment]())
					},
				), nil)
				assertFinalGraphUpdate(t, fixture, test.replacement, test.allowed)
			})
		})
	}
}

func TestRequiredSourceChildCreateAuthorizationTraversesBackToCompletedParent(t *testing.T) {
	for _, test := range []struct {
		name, title string
		allowed     bool
	}{{name: "final-pass", title: "allowed-parent", allowed: true}, {name: "final-fail", title: "blocked-parent", allowed: false}} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
				posts := golem.GeneratedToMany[graphMutationUser, graphMutationPost](fixture.schema.UserPosts, fixture.schema.Authorship)
				fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
					func(rules *golem.Rules[graphMutationUser]) {
						rules.CanCreate(posts.Some(fixture.postTitle.Eq("allowed-parent")))
					},
					func(rules *golem.Rules[graphMutationPost]) { rules.CanCreate(golem.All[graphMutationPost]()) },
					nil,
				), nil)

				input := finalGraphPostWithSourceAuthor(fixture, 41, 42, test.title)
				caller := mustFinalGraphCaller(t, fixture)
				_, err := CallerCreate(context.Background(), caller, fixture.postDescriptor, input)
				if test.allowed {
					if err != nil {
						t.Fatalf("required-source final graph create: %v", err)
					}
					assertGraphMutationRowsAndFacts(t, fixture, 1, 1, 0, 2)
					return
				}
				assertFinalGraphForbidden(t, err, "create")
				assertGraphMutationRowsAndFacts(t, fixture, 0, 0, 0, 0)
			})
		})
	}
}

func TestMixedSourceDependencyAndOrdinaryChildKeepGraphHookAndFactOrder(t *testing.T) {
	forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
		var lock sync.Mutex
		before, after, afterCommit := []string{}, []string{}, []string{}
		record := func(target *[]string, value string) {
			lock.Lock()
			defer lock.Unlock()
			*target = append(*target, value)
		}
		hooks := finalGraphCreateOrderHooks(fixture, record, &before, &after, &afterCommit)
		fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
			func(rules *golem.Rules[graphMutationUser]) { rules.CanCreate(golem.All[graphMutationUser]()) },
			func(rules *golem.Rules[graphMutationPost]) { rules.CanCreate(golem.All[graphMutationPost]()) },
			func(rules *golem.Rules[graphMutationComment]) { rules.CanCreate(golem.All[graphMutationComment]()) },
		), hooks)

		author := golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 52}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "source-author"),
		)
		comment := golem.GeneratedCreateInput[graphMutationComment](fixture.schema.Comment,
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: 53}),
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "ordinary-comment"),
		)
		post := golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 51}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "mixed-root"),
			golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, author),
			golem.GeneratedNestedCreate[graphMutationPost, graphMutationComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.Commenting, fixture.schema.Comment, comment),
		)
		if _, err := CallerCreate(context.Background(), mustFinalGraphCaller(t, fixture), fixture.postDescriptor, post); err != nil {
			t.Fatal(err)
		}

		if want := []string{"post", "user", "comment"}; !reflect.DeepEqual(before, want) {
			t.Fatalf("mixed before=%v want=%v", before, want)
		}
		if want := []string{"comment:53", "user:52", "post:51"}; !reflect.DeepEqual(after, want) || !reflect.DeepEqual(afterCommit, want) {
			t.Fatalf("mixed after=%v afterCommit=%v want=%v", after, afterCommit, want)
		}
		assertFinalGraphFactOrder(t, fixture, []finalGraphFactWant{
			{model: fixture.schema.Post, id: 51},
			{model: fixture.schema.User, id: 52},
			{model: fixture.schema.Comment, id: 53},
		})
	})
}

func TestOptionalSourceToOneNestedUpsertCreateAssignsParentAndObeysHooksFactsAndRollback(t *testing.T) {
	for _, test := range []struct {
		name, parentBody string
		allowed          bool
	}{{name: "commit", parentBody: "allowed-parent", allowed: true}, {name: "denied-child-rolls-back", parentBody: "denied-parent", allowed: false}} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			forEachFinalRecursiveProvider(t, func(t testing.TB, fixture recursiveMutationFixture) {
				var lock sync.Mutex
				before, after, afterCommit := []string{}, []string{}, []string{}
				record := func(target *[]string, value string) {
					lock.Lock()
					defer lock.Unlock()
					*target = append(*target, value)
				}
				hooks := finalRecursiveUpsertHooks(fixture, record, &before, &after, &afterCommit)
				fixture = reopenFinalRecursiveFixture(t, fixture, func(rules *golem.Rules[recursiveMutationComment]) {
					rules.CanCreate(fixture.body.Eq("denied-parent").Not())
				}, hooks)
				if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.descriptor, golem.GeneratedCreateInput(fixture.schema.Comment,
					golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.id, golem.UUID{15: 61}),
					golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.body, "root"),
				)); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.app.database.ExecContext(context.Background(), `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
					t.Fatal(err)
				}

				parentCreate := golem.GeneratedCreateInput[recursiveMutationComment](fixture.schema.Comment,
					golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.id, golem.UUID{15: 62}),
					golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.body, test.parentBody),
				)
				parentUpdate := golem.GeneratedUpdateInput[recursiveMutationComment](fixture.schema.Comment,
					golem.GeneratedSetFieldValue(fixture.schema.Comment, fixture.body, "unused-update"),
				)
				root := golem.GeneratedUpdateInput[recursiveMutationComment](fixture.schema.Comment,
					golem.GeneratedSetFieldValue(fixture.schema.Comment, fixture.body, "root-updated"),
					golem.GeneratedNestedUpsert[recursiveMutationComment, recursiveMutationComment](fixture.schema.Comment, fixture.schema.Parent, fixture.schema.Threading, fixture.schema.Comment, nil, parentCreate, parentUpdate),
				)
				rootTarget := golem.GeneratedUniqueSelectorValue[recursiveMutationComment](fixture.schema.Comment, fixture.schema.CommentKey,
					golem.GeneratedSelectorComponent(fixture.schema.CommentID, golem.UUID{15: 61}))
				caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
				if err != nil {
					t.Fatal(err)
				}
				_, err = CallerUpdate(context.Background(), caller, fixture.descriptor, rootTarget, root)
				if !test.allowed {
					assertFinalGraphForbidden(t, err, "create")
					assertFinalRecursiveRowsAndFacts(t, fixture, 1, 0)
					if want := []string{"update:61", "create:62"}; !reflect.DeepEqual(before, want) {
						t.Fatalf("denied optional-source before=%v want=%v", before, want)
					}
					var body string
					if scanErr := fixture.app.database.GetContext(context.Background(), &body, fixture.app.database.Rebind(`SELECT "body" FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` WHERE "id"=?`), mutationResultUUIDText(61)); scanErr != nil || body != "root" {
						t.Fatalf("denied optional-source root body=%q err=%v", body, scanErr)
					}
					if len(after) != 0 || len(afterCommit) != 0 {
						t.Fatalf("denied optional-source after=%v afterCommit=%v", after, afterCommit)
					}
					return
				}
				if err != nil {
					for cause := err; cause != nil; cause = errors.Unwrap(cause) {
						t.Logf("optional source upsert: %T: %v", cause, cause)
					}
					t.Fatal(err)
				}
				assertFinalRecursiveRowsAndFacts(t, fixture, 2, 2)
				var parentID *string
				query := fixture.app.database.Rebind(`SELECT "parent_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Comment) + ` WHERE "id"=?`)
				if scanErr := fixture.app.database.GetContext(context.Background(), &parentID, query, mutationResultUUIDText(61)); scanErr != nil || parentID == nil || *parentID != mutationResultUUIDText(62) {
					t.Fatalf("optional source parent=%v err=%v", parentID, scanErr)
				}
				if want := []string{"update:61", "create:62"}; !reflect.DeepEqual(before, want) {
					t.Fatalf("optional-source before=%v want=%v", before, want)
				}
				if want := []string{"create:62", "update:61"}; !reflect.DeepEqual(after, want) || !reflect.DeepEqual(afterCommit, want) {
					t.Fatalf("optional-source after=%v afterCommit=%v want=%v", after, afterCommit, want)
				}
				assertFinalRecursiveFactOrder(t, fixture, []byte{61, 62})
			})
		})
	}
}

func TestBeforeHookReplacementIntroducesSourceDependencyAndOrdinaryChildInCanonicalOrder(t *testing.T) {
	forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
		var lock sync.Mutex
		before, after, afterCommit := []string{}, []string{}, []string{}
		record := func(target *[]string, value string) {
			lock.Lock()
			defer lock.Unlock()
			*target = append(*target, value)
		}
		author := golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 82}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "replacement-author"),
		)
		comment := golem.GeneratedCreateInput[graphMutationComment](fixture.schema.Comment,
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: 83}),
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "replacement-comment"),
		)
		replacement := golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 81}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "replacement-root"),
			golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, author),
			golem.GeneratedNestedCreate[graphMutationPost, graphMutationComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.Commenting, fixture.schema.Comment, comment),
		)
		hooks := []golem.HookBinding[graphMutationActor]{
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[graphMutationPost]) error {
				record(&before, "post")
				request.ReplaceInput(replacement)
				return nil
			}),
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationUser]) error {
				record(&before, "user")
				return nil
			}),
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookRequest[graphMutationComment]](fixture.schema.Comment, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationComment]) error {
				record(&before, "comment")
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationPost]) error {
				record(&after, "post:"+finalGraphRowByte(result.Row(), fixture.postID))
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationUser]) error {
				record(&after, "user:"+finalGraphRowByte(result.Row(), fixture.userID))
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookResult[graphMutationComment]](fixture.schema.Comment, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationComment]) error {
				record(&after, "comment:"+finalGraphRowByte(result.Row(), fixture.commentID))
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationPost]) error {
				record(&afterCommit, "post:"+finalGraphRowByte(result.Row(), fixture.postID))
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationUser]) error {
				record(&afterCommit, "user:"+finalGraphRowByte(result.Row(), fixture.userID))
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookResult[graphMutationComment]](fixture.schema.Comment, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationComment]) error {
				record(&afterCommit, "comment:"+finalGraphRowByte(result.Row(), fixture.commentID))
				return nil
			}),
		}
		fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
			func(rules *golem.Rules[graphMutationUser]) { rules.CanCreate(golem.All[graphMutationUser]()) },
			func(rules *golem.Rules[graphMutationPost]) { rules.CanCreate(golem.All[graphMutationPost]()) },
			func(rules *golem.Rules[graphMutationComment]) { rules.CanCreate(golem.All[graphMutationComment]()) },
		), hooks)
		original := golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 81}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "original-without-nesting"),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, golem.GeneratedEqualField[graphMutationPost, golem.UUID](fixture.schema.AuthorID), golem.UUID{15: 99}),
		)
		if _, err := CallerCreate(context.Background(), mustFinalGraphCaller(t, fixture), fixture.postDescriptor, original); err != nil {
			t.Fatal(err)
		}
		if want := []string{"post", "user", "comment"}; !reflect.DeepEqual(before, want) {
			t.Fatalf("replacement before=%v want=%v", before, want)
		}
		if want := []string{"comment:83", "user:82", "post:81"}; !reflect.DeepEqual(after, want) || !reflect.DeepEqual(afterCommit, want) {
			t.Fatalf("replacement after=%v afterCommit=%v want=%v", after, afterCommit, want)
		}
		assertFinalGraphFactOrder(t, fixture, []finalGraphFactWant{{model: fixture.schema.Post, id: 81}, {model: fixture.schema.User, id: 82}, {model: fixture.schema.Comment, id: 83}})
	})
}

func TestNestedChildBeforeReplacementIntroducesDependencyAheadOfLowerFieldOrdinaryChildAndPreservesRootProjection(t *testing.T) {
	var before, after, afterCommit []string
	var lock sync.Mutex
	record := func(target *[]string, value string) {
		lock.Lock()
		defer lock.Unlock()
		*target = append(*target, value)
	}
	hookFactory := func(schema schematest.SocialMutationFixture) []golem.HookBinding[graphMutationActor] {
		postID := golem.GeneratedEqualField[socialMutationPost, golem.UUID](schema.PostID)
		commentID := golem.GeneratedEqualField[socialMutationComment, golem.UUID](schema.CommentID)
		commentBody := golem.GeneratedTextField[socialMutationComment, string](schema.CommentBody)
		userID := golem.GeneratedEqualField[socialMutationUser, golem.UUID](schema.UserID)
		userName := golem.GeneratedTextField[socialMutationUser, string](schema.UserName)
		return []golem.HookBinding[graphMutationActor]{
			golem.GeneratedBeforeHookBinding[graphMutationActor, socialMutationPost, golem.CreateHookRequest[socialMutationPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[socialMutationPost]) error {
				record(&before, "post:"+finalGraphCreateRequestByte(request, schema.PostID))
				return nil
			}),
			golem.GeneratedBeforeHookBinding[graphMutationActor, socialMutationComment, golem.CreateHookRequest[socialMutationComment]](schema.Comment, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[socialMutationComment]) error {
				id := finalGraphCreateRequestByte(request, schema.CommentID)
				record(&before, "comment:"+id)
				if id != "20" {
					return nil
				}
				author := golem.GeneratedCreateInput[socialMutationUser](schema.User,
					golem.GeneratedCreateFieldValue(schema.User, userID, golem.UUID{15: 2}),
					golem.GeneratedCreateFieldValue(schema.User, userName, "runtime-source-author"),
				)
				reply := golem.GeneratedCreateInput[socialMutationComment](schema.Comment,
					golem.GeneratedCreateFieldValue(schema.Comment, commentID, golem.UUID{15: 21}),
					golem.GeneratedCreateFieldValue(schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](schema.CommentPostID), golem.UUID{15: 10}),
					golem.GeneratedCreateFieldValue(schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](schema.CommentAuthorID), golem.UUID{15: 1}),
					golem.GeneratedCreateFieldValue(schema.Comment, commentBody, "runtime-ordinary-reply"),
				)
				// Intentionally place the lower-FieldID ordinary inverse before the
				// higher-FieldID required source. Canonical execution must still run
				// the source dependency first.
				replacement := golem.GeneratedCreateInput[socialMutationComment](schema.Comment,
					golem.GeneratedCreateFieldValue(schema.Comment, commentID, golem.UUID{15: 20}),
					golem.GeneratedCreateFieldValue(schema.Comment, commentBody, "runtime-replaced-comment"),
					golem.GeneratedNestedCreate[socialMutationComment, socialMutationComment](schema.Comment, schema.CommentReplies, schema.CommentThreading, schema.Comment, reply),
					golem.GeneratedNestedCreate[socialMutationComment, socialMutationUser](schema.Comment, schema.CommentAuthor, schema.CommentAuthorship, schema.User, author),
				)
				request.ReplaceInput(replacement)
				return nil
			}),
			golem.GeneratedBeforeHookBinding[graphMutationActor, socialMutationUser, golem.CreateHookRequest[socialMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[socialMutationUser]) error {
				record(&before, "user:"+finalGraphCreateRequestByte(request, schema.UserID))
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, socialMutationPost, golem.CreateHookResult[socialMutationPost]](schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[socialMutationPost]) error {
				record(&after, "post:"+finalGraphRowByte(result.Row(), postID))
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, socialMutationComment, golem.CreateHookResult[socialMutationComment]](schema.Comment, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[socialMutationComment]) error {
				record(&after, "comment:"+finalGraphRowByte(result.Row(), commentID))
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, socialMutationUser, golem.CreateHookResult[socialMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[socialMutationUser]) error {
				record(&after, "user:"+finalGraphRowByte(result.Row(), userID))
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, socialMutationPost, golem.CreateHookResult[socialMutationPost]](schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[socialMutationPost]) error {
				record(&afterCommit, "post:"+finalGraphRowByte(result.Row(), postID))
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, socialMutationComment, golem.CreateHookResult[socialMutationComment]](schema.Comment, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[socialMutationComment]) error {
				record(&afterCommit, "comment:"+finalGraphRowByte(result.Row(), commentID))
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, socialMutationUser, golem.CreateHookResult[socialMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[socialMutationUser]) error {
				record(&afterCommit, "user:"+finalGraphRowByte(result.Row(), userID))
				return nil
			}),
		}
	}
	forEachFinalAdversarialSocialProvider(t, hookFactory, func(t testing.TB, fixture socialMutationFixture) {
		if bytes.Compare(fixture.schema.CommentReplies[:], fixture.schema.CommentAuthor[:]) >= 0 {
			t.Fatalf("fixture is not adversarial: ordinary replies field=%x source author field=%x", fixture.schema.CommentReplies, fixture.schema.CommentAuthor)
		}
		lock.Lock()
		before, after, afterCommit = nil, nil, nil
		lock.Unlock()
		ctx := context.Background()
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userCreate(1, "seed-author")); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
			t.Fatal(err)
		}
		comment := golem.GeneratedCreateInput[socialMutationComment](fixture.schema.Comment,
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: 20}),
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](fixture.schema.CommentAuthorID), golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "untransformed-comment"),
		)
		post := golem.GeneratedCreateInput[socialMutationPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 10}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, golem.GeneratedEqualField[socialMutationPost, golem.UUID](fixture.schema.PostAuthorID), golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "projected-root"),
			golem.GeneratedNestedCreate[socialMutationPost, socialMutationComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.CommentPostRelation, fixture.schema.Comment, comment),
		)
		row, err := CallerCreate(ctx, func() *Caller[graphMutationPrincipal, graphMutationActor] {
			caller, callerErr := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
			if callerErr != nil {
				t.Fatal(callerErr)
			}
			return caller
		}(), fixture.postDescriptor, post, golem.Select[socialMutationPost](fixture.postTitle))
		if err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("nested runtime replacement: %T: %v", cause, cause)
			}
			t.Fatal(err)
		}
		if title, selected := golem.Value(row, fixture.postTitle).Get(); !selected || title != "projected-root" {
			t.Fatalf("runtime replacement root projection title=%q selected=%t", title, selected)
		}
		if want := []string{"post:10", "comment:20", "user:2", "comment:21"}; !reflect.DeepEqual(before, want) {
			t.Fatalf("runtime replacement before=%v want=%v", before, want)
		}
		assertFinalSocialFactOrder(t, fixture, []finalGraphFactWant{{model: fixture.schema.Post, id: 10}, {model: fixture.schema.Comment, id: 20}, {model: fixture.schema.User, id: 2}, {model: fixture.schema.Comment, id: 21}})
		if want := []string{"comment:21", "user:2", "comment:20", "post:10"}; !reflect.DeepEqual(after, want) || !reflect.DeepEqual(afterCommit, want) {
			t.Fatalf("runtime replacement after=%v afterCommit=%v want=%v", after, afterCommit, want)
		}
	})
}

func TestSameModelActionCreateManySiblingsKeepNodeScopedHookAndFactOrder(t *testing.T) {
	forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
		var lock sync.Mutex
		before, after, afterCommit := []string{}, []string{}, []string{}
		record := func(target *[]string, value string) {
			lock.Lock()
			defer lock.Unlock()
			*target = append(*target, value)
		}
		hooks := []golem.HookBinding[graphMutationActor]{
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationUser]) error {
				record(&before, "user")
				return nil
			}),
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[graphMutationPost]) error {
				record(&before, "post:"+finalGraphCreateRequestByte(request, fixture.schema.PostID))
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
				record(&after, "user")
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationPost]) error {
				record(&after, "post:"+finalGraphRowByte(result.Row(), fixture.postID))
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
				record(&afterCommit, "user")
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationPost]) error {
				record(&afterCommit, "post:"+finalGraphRowByte(result.Row(), fixture.postID))
				return nil
			}),
		}
		fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
			func(rules *golem.Rules[graphMutationUser]) { rules.CanCreate(golem.All[graphMutationUser]()) },
			func(rules *golem.Rules[graphMutationPost]) { rules.CanCreate(golem.All[graphMutationPost]()) },
			nil,
		), hooks)
		post := func(id byte) golem.CreateInput[graphMutationPost] {
			return golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, fmt.Sprintf("post-%d", id)),
			)
		}
		user := golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 70}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "batch-parent"),
			golem.GeneratedNestedCreateMany[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post(73), post(71), post(72)),
		)
		if _, err := CallerCreate(context.Background(), mustFinalGraphCaller(t, fixture), fixture.userDescriptor, user); err != nil {
			t.Fatal(err)
		}
		if want := []string{"user", "post:73", "post:71", "post:72"}; !reflect.DeepEqual(before, want) {
			t.Fatalf("siblings before=%v want=%v", before, want)
		}
		if want := []string{"post:72", "post:71", "post:73", "user"}; !reflect.DeepEqual(after, want) || !reflect.DeepEqual(afterCommit, want) {
			t.Fatalf("siblings after=%v afterCommit=%v want=%v", after, afterCommit, want)
		}
		assertFinalGraphFactOrder(t, fixture, []finalGraphFactWant{{model: fixture.schema.User, id: 70}, {model: fixture.schema.Post, id: 73}, {model: fixture.schema.Post, id: 71}, {model: fixture.schema.Post, id: 72}})
	})
}

func TestRootUpsertSelectedCreateSupportsEveryRequiredSourceDependencyAcrossProviders(t *testing.T) {
	for _, stance := range []string{"caller", "system"} {
		stance := stance
		for _, action := range []string{"create", "connect", "connect-or-create"} {
			action := action
			t.Run(stance+"-"+action, func(t *testing.T) {
				forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
					ctx := context.Background()
					userCreate := func(name string) golem.CreateInput[graphMutationUser] {
						return golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
							golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 92}),
							golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, name),
						)
					}
					userTarget := golem.GeneratedUniqueSelectorValue[graphMutationUser](fixture.schema.User, fixture.schema.UserKey,
						golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 92}))
					var nested golem.NestedValue[graphMutationPost]
					switch action {
					case "create":
						nested = golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userCreate("created-author"))
					case "connect":
						if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, userCreate("connected-author")); err != nil {
							t.Fatal(err)
						}
						if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
							t.Fatal(err)
						}
						nested = golem.GeneratedNestedConnect[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget)
					case "connect-or-create":
						nested = golem.GeneratedNestedConnectOrCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget, userCreate("coc-author"))
					default:
						t.Fatalf("unknown source dependency %q", action)
					}
					create := golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
						golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 91}),
						golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "selected-create"),
						nested,
					)
					update := golem.GeneratedUpdateInput[graphMutationPost](fixture.schema.Post,
						golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, "must-not-update"),
					)
					target := finalGraphPostTarget(fixture, 91)
					var err error
					if stance == "caller" {
						_, err = CallerUpsert(ctx, mustFinalGraphCaller(t, fixture), fixture.postDescriptor, target, create, update)
					} else {
						_, err = SystemUpsert(ctx, fixture.app.System(), fixture.postDescriptor, target, create, update)
					}
					if err != nil {
						for cause := err; cause != nil; cause = errors.Unwrap(cause) {
							t.Logf("root source upsert: %T: %v", cause, cause)
						}
						t.Fatal(err)
					}
					var author, title string
					query := fixture.app.database.Rebind(`SELECT "author_id","title" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
					if scanErr := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(91)).Scan(&author, &title); scanErr != nil || author != mutationResultUUIDText(92) || title != "selected-create" {
						t.Fatalf("root source upsert author=%q title=%q err=%v", author, title, scanErr)
					}
					wantFacts := 2
					if action == "connect" {
						wantFacts = 1
					}
					assertGraphMutationRowsAndFacts(t, fixture, 1, 1, 0, wantFacts)
				})
			})
		}
	}
}

func TestHookVetoWrappingRetryableProviderErrorNeverRetriesRootOrNestedUpsert(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		var before, after, afterCommit atomic.Int64
		veto := fmt.Errorf("root hook veto: %w", &pgconn.PgError{Code: "40001", Message: "hook-shaped serialization error"})
		fixture := newMutationResultFixtureWithHooks(t, MutationLimits{MaxUpsertAttempts: 3}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
			return []golem.HookBinding[mutationResultActor]{
				golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error { before.Add(1); return veto }),
				golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error { after.Add(1); return nil }),
				golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
					afterCommit.Add(1)
					return nil
				}),
			}
		}, func(context.Context, golem.AfterCommitFailure) {})
		_, err := CallerUpsert(context.Background(), mustMutationResultCaller(t, fixture), fixture.postDescriptor, fixture.target(96), fixture.createPost(96, golem.UUID{15: 1}, "vetoed"), fixture.updateTitle("unused"))
		assertFinalGraphBadInput(t, err)
		if before.Load() != 1 || after.Load() != 0 || afterCommit.Load() != 0 {
			t.Fatalf("root veto hooks before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
		}
		assertMutationResultTitleCount(t, fixture, "vetoed", 0)
		var facts int
		if queryErr := fixture.app.database.GetContext(context.Background(), &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); queryErr != nil || facts != 0 {
			t.Fatalf("root veto facts=%d err=%v", facts, queryErr)
		}
	})

	t.Run("nested-guarded-create-branch", func(t *testing.T) {
		var before, after, afterCommit atomic.Int64
		veto := fmt.Errorf("nested hook veto: %w", &pgconn.PgError{Code: "40001", Message: "hook-shaped serialization error"})
		fixture := newMutationResultFixtureWithHooks(t, MutationLimits{MaxUpsertAttempts: 3}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
			return []golem.HookBinding[mutationResultActor]{
				golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error { before.Add(1); return veto }),
				golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error { after.Add(1); return nil }),
				golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
					afterCommit.Add(1)
					return nil
				}),
			}
		}, func(context.Context, golem.AfterCommitFailure) {})
		ctx := context.Background()
		if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
			t.Fatal(err)
		}
		postTarget := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 97}))
		postCreate := golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 97}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "nested-vetoed"),
		)
		userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "must-roll-back"),
			golem.GeneratedNestedUpsert[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget, postCreate, fixture.updateTitle("unused")),
		)
		_, err := CallerUpdate(ctx, mustMutationResultCaller(t, fixture), fixture.userDescriptor, userTarget, input)
		assertFinalGraphBadInput(t, err)
		if before.Load() != 1 || after.Load() != 0 || afterCommit.Load() != 0 {
			t.Fatalf("nested veto hooks before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
		}
		assertMutationResultTitleCount(t, fixture, "nested-vetoed", 0)
		var name string
		if queryErr := fixture.app.database.GetContext(ctx, &name, `SELECT "name" FROM "users" WHERE "id"=?`, mutationResultUUIDText(1)); queryErr != nil || name != "alice" {
			t.Fatalf("nested veto user name=%q err=%v", name, queryErr)
		}
		var facts int
		if queryErr := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); queryErr != nil || facts != 0 {
			t.Fatalf("nested veto facts=%d err=%v", facts, queryErr)
		}
	})
}

func TestCallerUpsertSelectedUpdateBranchPreservesNestedExactErrorCodesAcrossProviders(t *testing.T) {
	for _, test := range []struct {
		name     string
		targetID byte
		wantCode golem.ErrorCode
		postRule func(graphMutationFixture, *golem.Rules[graphMutationPost])
	}{
		{name: "missing", targetID: 103, wantCode: golem.CodeNotFound, postRule: func(_ graphMutationFixture, rules *golem.Rules[graphMutationPost]) {
			rules.CanUpdate(golem.All[graphMutationPost]())
		}},
		{name: "invisible", targetID: 102, wantCode: golem.CodeNotFound, postRule: func(fixture graphMutationFixture, rules *golem.Rules[graphMutationPost]) {
			rules.CanUpdate(fixture.postTitle.Eq("visible"))
		}},
		{name: "row-postcondition-denied", targetID: 102, wantCode: golem.CodeForbidden, postRule: func(fixture graphMutationFixture, rules *golem.Rules[graphMutationPost]) {
			rules.CanUpdate(fixture.postTitle.Eq("before"))
		}},
		{name: "field-denied", targetID: 102, wantCode: golem.CodeForbidden, postRule: func(fixture graphMutationFixture, rules *golem.Rules[graphMutationPost]) {
			rules.CanUpdate(golem.All[graphMutationPost]())
			rules.CannotUpdateFields(golem.All[graphMutationPost](), fixture.postTitle)
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			forEachFinalGraphProvider(t, func(t testing.TB, fixture graphMutationFixture) {
				fixture = reopenFinalGraphFixture(t, fixture, finalGraphPolicies(t, fixture,
					func(rules *golem.Rules[graphMutationUser]) {
						rules.CanCreate(golem.All[graphMutationUser]())
						rules.CanUpdate(golem.All[graphMutationUser]())
					},
					func(rules *golem.Rules[graphMutationPost]) {
						rules.CanCreate(golem.All[graphMutationPost]())
						test.postRule(fixture, rules)
					},
					nil,
				), nil)
				ctx := context.Background()
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, golem.GeneratedCreateInput(fixture.schema.User,
					golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 101}),
					golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "before-user"),
				)); err != nil {
					t.Fatal(err)
				}
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, golem.GeneratedCreateInput(fixture.schema.Post,
					golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 102}),
					golem.GeneratedCreateFieldValue(fixture.schema.Post, golem.GeneratedEqualField[graphMutationPost, golem.UUID](fixture.schema.AuthorID), golem.UUID{15: 101}),
					golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "before"),
				)); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
					t.Fatal(err)
				}
				create := golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
					golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 101}),
					golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "unused-create"),
				)
				child := golem.GeneratedNestedUpdate[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post,
					finalGraphPostTarget(fixture, test.targetID),
					golem.GeneratedUpdateInput(fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, "after")),
				)
				update := golem.GeneratedUpdateInput[graphMutationUser](fixture.schema.User,
					golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "after-user"), child,
				)
				userTarget := golem.GeneratedUniqueSelectorValue[graphMutationUser](fixture.schema.User, fixture.schema.UserKey,
					golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 101}))
				_, err := CallerUpsert(ctx, mustFinalGraphCaller(t, fixture), fixture.userDescriptor, userTarget, create, update)
				var failure *golem.Error
				if !errors.As(err, &failure) || failure.Code != test.wantCode || failure.Operation != "upsert" {
					t.Fatalf("selected update failure=%#v err=%v want=%s/upsert", failure, err, test.wantCode)
				}
				var userName, postTitle string
				userQuery := fixture.app.database.Rebind(`SELECT "name" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.User) + ` WHERE "id"=?`)
				postQuery := fixture.app.database.Rebind(`SELECT "title" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
				if queryErr := fixture.app.database.GetContext(ctx, &userName, userQuery, mutationResultUUIDText(101)); queryErr != nil || userName != "before-user" {
					t.Fatalf("selected update rollback user=%q err=%v", userName, queryErr)
				}
				if queryErr := fixture.app.database.GetContext(ctx, &postTitle, postQuery, mutationResultUUIDText(102)); queryErr != nil || postTitle != "before" {
					t.Fatalf("selected update rollback post=%q err=%v", postTitle, queryErr)
				}
				var facts int
				if queryErr := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); queryErr != nil || facts != 0 {
					t.Fatalf("selected update facts=%d err=%v", facts, queryErr)
				}
			})
		})
	}
}

func assertFinalGraphUserCreate(t testing.TB, fixture graphMutationFixture, relationAction, title string, allowed bool) {
	t.Helper()
	ctx := context.Background()
	var nested golem.NestedValue[graphMutationUser]
	if relationAction == "create" {
		post := golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 12}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, title),
		)
		nested = golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post)
	} else {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, golem.GeneratedCreateInput(fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 9}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "seed-owner"),
		)); err != nil {
			t.Fatal(err)
		}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, golem.GeneratedCreateInput(fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 12}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, golem.GeneratedEqualField[graphMutationPost, golem.UUID](fixture.schema.AuthorID), golem.UUID{15: 9}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, title),
		)); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
			t.Fatal(err)
		}
		nested = golem.GeneratedNestedConnect[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, finalGraphPostTarget(fixture, 12))
	}
	user := golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 11}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "candidate"),
		nested,
	)
	_, err := CallerCreate(ctx, mustFinalGraphCaller(t, fixture), fixture.userDescriptor, user)
	if allowed {
		if err != nil {
			t.Fatalf("completed relation create: %v", err)
		}
		var author string
		query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
		if scanErr := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(12)); scanErr != nil || author != mutationResultUUIDText(11) {
			t.Fatalf("completed relation author=%q err=%v", author, scanErr)
		}
		return
	}
	assertFinalGraphForbidden(t, err, "create")
	var users int
	query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.User) + ` WHERE "id"=?`)
	if scanErr := fixture.app.database.GetContext(ctx, &users, query, mutationResultUUIDText(11)); scanErr != nil || users != 0 {
		t.Fatalf("denied completed relation user rows=%d err=%v", users, scanErr)
	}
	if relationAction == "create" {
		assertGraphMutationRowsAndFacts(t, fixture, 0, 0, 0, 0)
		return
	}
	var author string
	query = fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
	if scanErr := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(12)); scanErr != nil || author != mutationResultUUIDText(9) {
		t.Fatalf("denied connect author=%q err=%v", author, scanErr)
	}
	var facts int
	if scanErr := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); scanErr != nil || facts != 0 {
		t.Fatalf("denied connect facts=%d err=%v", facts, scanErr)
	}
}

func assertFinalGraphUpdate(t testing.TB, fixture graphMutationFixture, replacement string, allowed bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, golem.GeneratedCreateInput(fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 21}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "owner"),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, golem.GeneratedCreateInput(fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 22}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, golem.GeneratedEqualField[graphMutationPost, golem.UUID](fixture.schema.AuthorID), golem.UUID{15: 21}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "before-title"),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.commentDescriptor, golem.GeneratedCreateInput(fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: 23}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[graphMutationComment, golem.UUID](fixture.schema.CommentPostID), golem.UUID{15: 22}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "old"),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	newComment := golem.GeneratedCreateInput[graphMutationComment](fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: 24}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, replacement),
	)
	input := golem.GeneratedUpdateInput[graphMutationPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, "after-title"),
		golem.GeneratedNestedDelete[graphMutationPost, graphMutationComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.Commenting, fixture.schema.Comment, finalGraphCommentTarget(fixture, 23)),
		golem.GeneratedNestedCreate[graphMutationPost, graphMutationComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.Commenting, fixture.schema.Comment, newComment),
	)
	_, err := CallerUpdate(ctx, mustFinalGraphCaller(t, fixture), fixture.postDescriptor, finalGraphPostTarget(fixture, 22), input)
	if allowed {
		if err != nil {
			t.Fatalf("completed update graph: %v", err)
		}
		assertFinalGraphPostAndComments(t, fixture, "after-title", map[byte]string{24: "new"}, 3)
		return
	}
	assertFinalGraphForbidden(t, err, "update")
	assertFinalGraphPostAndComments(t, fixture, "before-title", map[byte]string{23: "old"}, 0)
}

func assertFinalGraphPostAndComments(t testing.TB, fixture graphMutationFixture, wantTitle string, comments map[byte]string, facts int) {
	t.Helper()
	ctx := context.Background()
	var title string
	query := fixture.app.database.Rebind(`SELECT "title" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &title, query, mutationResultUUIDText(22)); err != nil || title != wantTitle {
		t.Fatalf("post title=%q want=%q err=%v", title, wantTitle, err)
	}
	type row struct {
		ID   string `db:"id"`
		Body string `db:"body"`
	}
	var rows []row
	if err := fixture.app.database.SelectContext(ctx, &rows, `SELECT "id","body" FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` ORDER BY "id"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(comments) {
		t.Fatalf("comments=%#v want=%v", rows, comments)
	}
	for _, value := range rows {
		matched := false
		for id, body := range comments {
			if value.ID == mutationResultUUIDText(id) && value.Body == body {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("unexpected comment=%#v want=%v", value, comments)
		}
	}
	var gotFacts int
	if err := fixture.app.database.GetContext(ctx, &gotFacts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || gotFacts != facts {
		t.Fatalf("update facts=%d want=%d err=%v", gotFacts, facts, err)
	}
}

func finalGraphPostWithSourceAuthor(fixture graphMutationFixture, postID, userID byte, title string) golem.CreateInput[graphMutationPost] {
	author := golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: userID}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "source-author"),
	)
	return golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: postID}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, title),
		golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, author),
	)
}

func finalGraphCreateOrderHooks(fixture graphMutationFixture, record func(*[]string, string), before, after, afterCommit *[]string) []golem.HookBinding[graphMutationActor] {
	return []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
			record(before, "post")
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationUser]) error {
			record(before, "user")
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookRequest[graphMutationComment]](fixture.schema.Comment, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationComment]) error {
			record(before, "comment")
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationPost]) error {
			record(after, "post:"+finalGraphRowByte(result.Row(), fixture.postID))
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationUser]) error {
			record(after, "user:"+finalGraphRowByte(result.Row(), fixture.userID))
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookResult[graphMutationComment]](fixture.schema.Comment, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationComment]) error {
			record(after, "comment:"+finalGraphRowByte(result.Row(), fixture.commentID))
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](fixture.schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationPost]) error {
			record(afterCommit, "post:"+finalGraphRowByte(result.Row(), fixture.postID))
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](fixture.schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationUser]) error {
			record(afterCommit, "user:"+finalGraphRowByte(result.Row(), fixture.userID))
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookResult[graphMutationComment]](fixture.schema.Comment, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationComment]) error {
			record(afterCommit, "comment:"+finalGraphRowByte(result.Row(), fixture.commentID))
			return nil
		}),
	}
}

func finalGraphRowByte[M any](row golem.Row[M], field golem.EqualField[M, golem.UUID]) string {
	value, _ := golem.Value(row, field).Get()
	return fmt.Sprintf("%d", value[15])
}

func finalGraphCreateRequestByte[M any](request *golem.CreateHookRequest[M], field golem.FieldID) string {
	if request == nil {
		return "missing"
	}
	frozen, err := golem.RuntimeFreezeCreateInput(request.Input())
	if err != nil {
		return "invalid"
	}
	for _, value := range frozen.Fields() {
		if value.FieldID() != field {
			continue
		}
		operand, ok := value.Value()
		if !ok {
			return "null"
		}
		switch typed := operand.(type) {
		case golem.UUID:
			return fmt.Sprintf("%d", typed[15])
		case [16]byte:
			return fmt.Sprintf("%d", typed[15])
		default:
			return fmt.Sprintf("%T", operand)
		}
	}
	return "absent"
}

type finalGraphFactWant struct {
	model golem.ModelID
	id    byte
}

func assertFinalGraphFactOrder(t testing.TB, fixture graphMutationFixture, want []finalGraphFactWant) {
	t.Helper()
	type row struct {
		Model    string `db:"model_id"`
		Ordinal  int64  `db:"transaction_ordinal"`
		Metadata []byte `db:"metadata"`
	}
	var rows []row
	if err := fixture.app.database.Select(&rows, `SELECT "model_id","transaction_ordinal","metadata" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("facts=%d want=%d", len(rows), len(want))
	}
	for index, expected := range want {
		if rows[index].Model != hex.EncodeToString(expected.model[:]) || rows[index].Ordinal != int64(index+1) {
			t.Fatalf("fact[%d]=%#v want model=%x ordinal=%d", index, rows[index], expected.model, index+1)
		}
		envelope, err := mutationfact.Decode(rows[index].Metadata, fixture.schema.Registry)
		if err != nil {
			t.Fatal(err)
		}
		identity, ok := envelope.AfterIdentity()
		if !ok || len(identity.Components()) != 1 {
			t.Fatalf("fact[%d] identity=%#v present=%t", index, identity, ok)
		}
		value, ok := identity.Components()[0].PolicyValue()
		if !ok || !mutationdecode.EqualValue(value, policyir.UUIDValue([16]byte(golem.UUID{15: expected.id}))) {
			t.Fatalf("fact[%d] identity value=%#v present=%t want=%d", index, value, ok, expected.id)
		}
	}
}

func assertFinalSocialFactOrder(t testing.TB, fixture socialMutationFixture, want []finalGraphFactWant) {
	t.Helper()
	type row struct {
		Model    string `db:"model_id"`
		Ordinal  int64  `db:"transaction_ordinal"`
		Metadata []byte `db:"metadata"`
	}
	var rows []row
	if err := fixture.app.database.Select(&rows, `SELECT "model_id","transaction_ordinal","metadata" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("social ordered facts=%d want=%d", len(rows), len(want))
	}
	for index, expected := range want {
		if rows[index].Model != hex.EncodeToString(expected.model[:]) || rows[index].Ordinal != int64(index+1) {
			t.Fatalf("social fact[%d]=%#v want model=%x ordinal=%d", index, rows[index], expected.model, index+1)
		}
		envelope, err := mutationfact.Decode(rows[index].Metadata, fixture.schema.Registry)
		if err != nil {
			t.Fatal(err)
		}
		identity, ok := envelope.AfterIdentity()
		if !ok || len(identity.Components()) != 1 {
			t.Fatalf("social fact[%d] identity=%#v present=%t", index, identity, ok)
		}
		value, ok := identity.Components()[0].PolicyValue()
		if !ok || !mutationdecode.EqualValue(value, policyir.UUIDValue([16]byte(golem.UUID{15: expected.id}))) {
			t.Fatalf("social fact[%d] identity value=%#v present=%t want=%d", index, value, ok, expected.id)
		}
	}
}

func finalGraphPolicies(t testing.TB, fixture graphMutationFixture, user func(*golem.Rules[graphMutationUser]), post func(*golem.Rules[graphMutationPost]), comment func(*golem.Rules[graphMutationComment])) []golem.PolicyBinding[graphMutationActor] {
	t.Helper()
	userBinding := golem.GeneratedPolicyBinding[graphMutationActor, graphMutationUser](fixture.schema.User, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphMutationUser]()
		rules.CanRead(golem.All[graphMutationUser]())
		rules.CanUpdate(golem.All[graphMutationUser]())
		rules.CanDelete(golem.All[graphMutationUser]())
		if user != nil {
			user(rules)
		}
		return rules.Freeze(fixture.schema.User)
	})
	postBinding := golem.GeneratedPolicyBinding[graphMutationActor, graphMutationPost](fixture.schema.Post, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphMutationPost]()
		rules.CanRead(golem.All[graphMutationPost]())
		rules.CanDelete(golem.All[graphMutationPost]())
		if post != nil {
			post(rules)
		}
		return rules.Freeze(fixture.schema.Post)
	})
	commentBinding := golem.GeneratedPolicyBinding[graphMutationActor, graphMutationComment](fixture.schema.Comment, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphMutationComment]()
		rules.CanRead(golem.All[graphMutationComment]())
		rules.CanUpdate(golem.All[graphMutationComment]())
		if comment != nil {
			comment(rules)
		}
		return rules.Freeze(fixture.schema.Comment)
	})
	return []golem.PolicyBinding[graphMutationActor]{userBinding, postBinding, commentBinding}
}

func reopenFinalGraphFixture(t testing.TB, fixture graphMutationFixture, policies []golem.PolicyBinding[graphMutationActor], hooks []golem.HookBinding[graphMutationActor]) graphMutationFixture {
	t.Helper()
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), policies, hooks))
	if err != nil {
		t.Fatal(err)
	}
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), Config[graphMutationPrincipal, graphMutationActor]{
		DB: fixture.app.database, Provider: provider, Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: func(context.Context, graphMutationPrincipal) (graphMutationActor, error) {
			return graphMutationActor{}, nil
		},
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func reopenFinalRecursiveFixture(t testing.TB, fixture recursiveMutationFixture, configure func(*golem.Rules[recursiveMutationComment]), hooks []golem.HookBinding[graphMutationActor]) recursiveMutationFixture {
	t.Helper()
	policy := golem.GeneratedPolicyBinding[graphMutationActor, recursiveMutationComment](fixture.schema.Comment, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[recursiveMutationComment]()
		rules.CanRead(golem.All[recursiveMutationComment]())
		rules.CanUpdate(golem.All[recursiveMutationComment]())
		rules.CanDelete(golem.All[recursiveMutationComment]())
		configure(rules)
		return rules.Freeze(fixture.schema.Comment)
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[graphMutationActor]{policy}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), Config[graphMutationPrincipal, graphMutationActor]{
		DB: fixture.app.database, Provider: provider, Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: func(context.Context, graphMutationPrincipal) (graphMutationActor, error) {
			return graphMutationActor{}, nil
		},
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func finalRecursiveUpsertHooks(fixture recursiveMutationFixture, record func(*[]string, string), before, after, afterCommit *[]string) []golem.HookBinding[graphMutationActor] {
	return []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, recursiveMutationComment, golem.UpdateHookRequest[recursiveMutationComment]](fixture.schema.Comment, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[recursiveMutationComment]) error {
			record(before, "update:61")
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, recursiveMutationComment, golem.CreateHookRequest[recursiveMutationComment]](fixture.schema.Comment, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[recursiveMutationComment]) error {
			record(before, "create:"+finalGraphCreateRequestByte(request, fixture.schema.CommentID))
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, recursiveMutationComment, golem.UpdateHookResult[recursiveMutationComment]](fixture.schema.Comment, golem.HookUpdate, func(_ context.Context, result golem.UpdateHookResult[recursiveMutationComment]) error {
			record(after, "update:"+finalGraphRowByte(result.After(), fixture.id))
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, recursiveMutationComment, golem.CreateHookResult[recursiveMutationComment]](fixture.schema.Comment, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[recursiveMutationComment]) error {
			record(after, "create:"+finalGraphRowByte(result.Row(), fixture.id))
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, recursiveMutationComment, golem.UpdateHookResult[recursiveMutationComment]](fixture.schema.Comment, golem.HookUpdate, func(_ context.Context, result golem.UpdateHookResult[recursiveMutationComment]) error {
			record(afterCommit, "update:"+finalGraphRowByte(result.After(), fixture.id))
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, recursiveMutationComment, golem.CreateHookResult[recursiveMutationComment]](fixture.schema.Comment, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[recursiveMutationComment]) error {
			record(afterCommit, "create:"+finalGraphRowByte(result.Row(), fixture.id))
			return nil
		}),
	}
}

func assertFinalRecursiveRowsAndFacts(t testing.TB, fixture recursiveMutationFixture, wantRows, wantFacts int) {
	t.Helper()
	var rows, facts int
	if err := fixture.app.database.Get(&rows, `SELECT COUNT(*) FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)); err != nil || rows != wantRows {
		t.Fatalf("recursive rows=%d want=%d err=%v", rows, wantRows, err)
	}
	if err := fixture.app.database.Get(&facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || facts != wantFacts {
		t.Fatalf("recursive facts=%d want=%d err=%v", facts, wantFacts, err)
	}
}

func assertFinalRecursiveFactOrder(t testing.TB, fixture recursiveMutationFixture, want []byte) {
	t.Helper()
	type row struct {
		Ordinal  int64  `db:"transaction_ordinal"`
		Metadata []byte `db:"metadata"`
	}
	var rows []row
	if err := fixture.app.database.Select(&rows, `SELECT "transaction_ordinal","metadata" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("recursive ordered facts=%d want=%d", len(rows), len(want))
	}
	for index, expected := range want {
		envelope, err := mutationfact.Decode(rows[index].Metadata, fixture.schema.Registry)
		if err != nil {
			t.Fatal(err)
		}
		identity, ok := envelope.AfterIdentity()
		if !ok || len(identity.Components()) != 1 || rows[index].Ordinal != int64(index+1) {
			t.Fatalf("recursive fact[%d] ordinal=%d identity=%#v present=%t", index, rows[index].Ordinal, identity, ok)
		}
		value, ok := identity.Components()[0].PolicyValue()
		if !ok || !mutationdecode.EqualValue(value, policyir.UUIDValue([16]byte(golem.UUID{15: expected}))) {
			t.Fatalf("recursive fact[%d] value=%#v present=%t want=%d", index, value, ok, expected)
		}
	}
}

func forEachFinalGraphProvider(t *testing.T, run func(testing.TB, graphMutationFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		run(t, newGraphMutationFixture(t, schematest.NewSubscribedGraph(t), golem.ModelID{}))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			run(t, newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, nil))
		})
	}
}

func forEachFinalRecursiveProvider(t *testing.T, run func(testing.TB, recursiveMutationFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { run(t, newRecursiveMutationFixture(t)) })
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			run(t, newPostgresRecursiveMutationFixture(t, profile))
		})
	}
}

func forEachFinalAdversarialSocialProvider(t *testing.T, hookFactory func(schematest.SocialMutationFixture) []golem.HookBinding[graphMutationActor], run func(testing.TB, socialMutationFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		schema := schematest.NewSubscribedSocialMutationAdversarialRelationOrder(t)
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "social-runtime-replacement.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
			t.Fatal(err)
		}
		run(t, openSocialMutationFixture(t, database, golem.SQLite, schema, golem.ModelID{}, hookFactory(schema)))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			ctx := context.Background()
			suffix := time.Now().UnixNano()
			namespace := physical.PhysicalName(fmt.Sprintf("p4dyn_%s_%d_%d", profile.name, os.Getpid(), suffix))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("p4dyn_sys_%s_%d_%d", profile.name, os.Getpid(), suffix))
			schema := schematest.NewSubscribedSocialMutationAdversarialRelationOrderPostgreSQLNamespaces(t, namespace, systemNamespace)
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schema.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			run(t, openSocialMutationFixture(t, database, golem.PostgreSQL, schema, golem.ModelID{}, hookFactory(schema)))
		})
	}
}

func mustFinalGraphCaller(t testing.TB, fixture graphMutationFixture) *Caller[graphMutationPrincipal, graphMutationActor] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func finalGraphPostTarget(fixture graphMutationFixture, id byte) golem.MutationTarget[graphMutationPost] {
	return golem.GeneratedUniqueSelectorValue[graphMutationPost](fixture.schema.Post, fixture.schema.PostKey,
		golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
}

func finalGraphCommentTarget(fixture graphMutationFixture, id byte) golem.MutationTarget[graphMutationComment] {
	return golem.GeneratedUniqueSelectorValue[graphMutationComment](fixture.schema.Comment, fixture.schema.CommentKey,
		golem.GeneratedSelectorComponent(fixture.schema.CommentID, golem.UUID{15: id}))
}

func assertFinalGraphForbidden(t testing.TB, err error, operation string) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Operation != operation {
		t.Fatalf("authorization failure=%#v err=%v want FORBIDDEN operation=%s", failure, err, operation)
	}
}

func assertFinalGraphBadInput(t testing.TB, err error) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("hook veto failure=%#v err=%v want BAD_USER_INPUT", failure, err)
	}
}
