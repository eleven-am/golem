package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
)

const rowVersionPerKeyDuration = 30 * time.Second

func requirePrimaryPostgreSQL(t *testing.T, profile mutationProviderAcceptanceFixture) {
	t.Helper()
	if profile.provider != golem.PostgreSQL || strings.Contains(t.Name(), "linguistic") {
		t.Skip("PostgreSQL primary profile only")
	}
}

func TestRowVersionIncreasesPerKey(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionRestrict, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		requirePrimaryPostgreSQL(t, profile)
		ctx, fixture := context.Background(), profile.fixture
		fixture.app.database.SetMaxOpenConns(8)
		system := fixture.app.System()
		alice := golem.UUID{15: 1}
		const keys, writers = 8, 64
		for key := 0; key < keys; key++ {
			if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(byte(100+key), alice, "seed")); err != nil {
				t.Fatal(err)
			}
		}
		duration := rowVersionPerKeyDuration
		if testing.Short() {
			duration = 3 * time.Second
		}
		deadline := time.Now().Add(duration)
		failures := make(chan string, 1)
		report := func(message string) {
			select {
			case failures <- message:
			default:
			}
		}
		var group sync.WaitGroup
		for writer := 0; writer < writers; writer++ {
			group.Add(1)
			go func(writer int) {
				defer group.Done()
				random := rand.New(rand.NewSource(int64(writer)))
				for time.Now().Before(deadline) {
					id := byte(100 + writer%keys)
					title := fmt.Sprintf("writer-%d-%d", writer, random.Int())
					if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(id), fixture.updateTitle(title)); err != nil {
						report(fmt.Sprintf("writer %d: %v | chain: %s", writer, err, unwrapChain(err)))
						return
					}
				}
			}(writer)
		}
		polled := make(chan struct{})
		go func() {
			defer close(polled)
			last := map[byte]int64{}
			for time.Now().Before(deadline) {
				rows, err := fixture.app.database.Query(`SELECT "id", "golem_version" FROM ` + profile.posts)
				if err != nil {
					report(fmt.Sprintf("poll: %v", err))
					return
				}
				for rows.Next() {
					var id string
					var version int64
					if err := rows.Scan(&id, &version); err != nil {
						rows.Close()
						report(fmt.Sprintf("poll scan: %v", err))
						return
					}
					key := postIDByte(t, id)
					if version < last[key] {
						rows.Close()
						report(fmt.Sprintf("key %d: committed version fell from %d to %d", key, last[key], version))
						return
					}
					last[key] = version
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					report(fmt.Sprintf("poll rows: %v", err))
					return
				}
				rows.Close()
			}
		}()
		group.Wait()
		<-polled
		select {
		case message := <-failures:
			t.Fatal(message)
		default:
		}
	})
}

func TestRecreatedKeyVersionExceedsDelete(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionRestrict, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		requirePrimaryPostgreSQL(t, profile)
		ctx, fixture := context.Background(), profile.fixture
		system := fixture.app.System()
		alice := golem.UUID{15: 1}
		namespace := strings.TrimSuffix(profile.posts, `."posts"`)
		for _, statement := range []string{
			`CREATE FUNCTION ` + namespace + `.golem_test_slow_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.title = 'slow' THEN PERFORM pg_sleep(1.5); END IF; RETURN NEW; END $$`,
			`CREATE TRIGGER golem_test_slow_insert BEFORE INSERT ON ` + profile.posts + ` FOR EACH ROW EXECUTE FUNCTION ` + namespace + `.golem_test_slow_insert()`,
		} {
			if _, err := fixture.app.database.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(40, alice, "first")); err != nil {
			t.Fatal(err)
		}
		recreated := make(chan error, 1)
		go func() {
			_, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(40, alice, "slow"))
			recreated <- err
		}()
		time.Sleep(400 * time.Millisecond)
		if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(40), fixture.updateTitle("while the insert waits")); err != nil {
			t.Fatal(err)
		}
		lastVersion := postVersions(t, profile)[40]
		if _, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(40)); err != nil {
			t.Fatal(err)
		}
		if err := <-recreated; err != nil {
			t.Fatalf("recreating the key: %v", err)
		}
		requireRise(t, "recreated key against its delete", lastVersion, postVersions(t, profile)[40])
	})
}

func TestVersionSequenceCacheDrift(t *testing.T) {
	rowVersionRestrictFixtures(t, compilerir.ActionRestrict, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		requirePrimaryPostgreSQL(t, profile)
		fixture := profile.fixture
		sequence := fixture.schema.PostgreSQL.System.Namespace.Name
		qualified := `"` + string(sequence) + `"."golem_row_version"`
		if _, err := fixture.app.database.Exec(`ALTER SEQUENCE ` + qualified + ` CACHE 20`); err != nil {
			t.Fatal(err)
		}
		_, err := postgresprovider.New().Introspect(context.Background(), fixture.app.database, fixture.schema.PostgreSQL)
		if err == nil || !strings.Contains(err.Error(), "CACHE 1") {
			t.Fatalf("a CACHE 20 sequence was accepted: %v", err)
		}
		if _, err := fixture.app.database.Exec(`ALTER SEQUENCE ` + qualified + ` CACHE 1`); err != nil {
			t.Fatal(err)
		}
		if _, err := postgresprovider.New().Introspect(context.Background(), fixture.app.database, fixture.schema.PostgreSQL); err != nil {
			t.Fatalf("the CACHE 1 sequence was refused: %v", err)
		}
	})
}

func unwrapChain(err error) string {
	var parts []string
	for current := err; current != nil; current = errors.Unwrap(current) {
		parts = append(parts, fmt.Sprintf("%T: %v", current, current))
	}
	return strings.Join(parts, " <- ")
}
