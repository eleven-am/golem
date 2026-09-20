package runtime

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"testing"

	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
)

const (
	sqliteOracleRows = 5000
	sqliteOracleTied = 4600
)

func seedSQLiteOracleRows(t *testing.T, fixture drainFixture) {
	t.Helper()
	index, ok := fixture.manager.index("post", "related")
	if !ok {
		t.Fatal("semantic index is absent")
	}
	if _, err := fixture.db.Exec(`DELETE FROM "posts"; DELETE FROM "` + drainStateTable + `"; DELETE FROM "` + drainVectorTable + `"`); err != nil {
		t.Fatal(err)
	}
	fingerprint := hex.EncodeToString(index.SpaceFingerprint[:])
	transaction, err := fixture.db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	for _, ordinal := range rand.New(rand.NewSource(20260919)).Perm(sqliteOracleRows) {
		id := fmt.Sprintf("k%06d", ordinal)
		values := []float32{1, 0, 0}
		if ordinal >= sqliteOracleTied {
			values = []float32{1, float32(ordinal-sqliteOracleTied+1) / 100, 0}
		}
		vector, err := sqlitevec.Serialize(values, 3)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.Exec(`INSERT INTO "posts" (id,title) VALUES (?,?)`, id, strconv.Itoa(ordinal%10)); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.Exec(`INSERT INTO "`+drainStateTable+`" (record_key,source_hash,space_fingerprint,status,updated_at,"id") VALUES (?,x'01',?,'ready',1,?)`, id, fingerprint, id); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.Exec(`INSERT INTO "`+drainVectorTable+`" (record_key,embedding) VALUES (?,?)`, id, vector); err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func sqliteOracleCandidates() Candidates {
	return textCandidates(`SELECT golem_r0."id" AS "id" FROM "posts" AS golem_r0 WHERE golem_r0."title" <> ?`, "3")
}

func exactSQLiteOraclePage(t *testing.T, fixture drainFixture, vector []byte, exclude string, take int) []Rank {
	t.Helper()
	index, _ := fixture.manager.index("post", "related")
	statement := `SELECT golem_ov.record_key,vec_distance_cosine(golem_ov.embedding,?1) AS distance,golem_os."id"` +
		` FROM "` + drainVectorTable + `" AS golem_ov` +
		` JOIN "` + drainStateTable + `" AS golem_os ON golem_os.record_key=golem_ov.record_key` +
		` JOIN "posts" AS golem_od ON golem_od."id"=golem_os."id"` +
		` WHERE golem_od."title"<>'3' AND golem_os.space_fingerprint=?2 AND golem_os.status='ready' AND golem_ov.record_key<>?3` +
		` ORDER BY distance,golem_ov.record_key LIMIT ?4`
	rows, err := fixture.db.Queryx(statement, vector, hex.EncodeToString(index.SpaceFingerprint[:]), exclude, take)
	if err != nil {
		t.Fatal(err)
	}
	ranks, err := decodeRanks(rows, take, textCandidates(""))
	if closeErr := rows.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	return ranks
}

func TestSQLiteRankMatchesTheExactPageAcrossTiedDistances(t *testing.T) {
	fixture := newDrainFixture(t)
	seedSQLiteOracleRows(t, fixture)
	index, ok := fixture.manager.index("post", "related")
	if !ok {
		t.Fatal("semantic index is absent")
	}
	vector, err := sqlitevec.Serialize([]float32{1, 0, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		exclude string
		take    int
		first   string
	}{
		{take: 1, first: "k000000"},
		{take: 20, first: "k000000"},
		{take: 200, first: "k000000"},
		{take: MaximumResults, first: "k000000"},
		{exclude: "k000000", take: 20, first: "k000001"},
	} {
		t.Run(fmt.Sprintf("take=%d/exclude=%q", test.take, test.exclude), func(t *testing.T) {
			exact := exactSQLiteOraclePage(t, fixture, vector, test.exclude, test.take)
			if len(exact) != test.take || exact[0].Key != test.first {
				t.Fatalf("the oracle is not the exact page: len=%d first=%s", len(exact), rankPageDigest(exact[:1]))
			}
			ranks, err := fixture.manager.rankVector(context.Background(), index, vector, sqliteOracleCandidates(), test.exclude, test.take)
			if err != nil {
				t.Fatal(err)
			}
			if len(ranks) != len(exact) {
				t.Fatalf("served page=%d want=%d", len(ranks), len(exact))
			}
			for position := range exact {
				if ranks[position].Key == exact[position].Key && math.Abs(ranks[position].Distance-exact[position].Distance) <= 1e-9 {
					continue
				}
				t.Fatalf("served page diverges from ground truth at %d\nserved: %s\nexact:  %s", position, rankPageDigest(ranks[:min(len(ranks), 20)]), rankPageDigest(exact[:min(len(exact), 20)]))
			}
		})
	}
}
