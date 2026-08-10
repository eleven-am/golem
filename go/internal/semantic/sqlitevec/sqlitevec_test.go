package sqlitevec

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEmbeddedSQLiteVecCosineKNN(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "semantic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if version, err := Probe(context.Background(), database); err != nil || version == "" {
		var raw string
		rawErr := database.QueryRowContext(context.Background(), "SELECT vec_version()").Scan(&raw)
		t.Fatalf("probe version=%q err=%v raw=%q rawErr=%v", version, err, raw, rawErr)
	}
	const dimensions = 3
	if _, err := database.Exec(`CREATE VIRTUAL TABLE vec_posts USING vec0(record_id INTEGER PRIMARY KEY, embedding float[3] distance_metric=cosine)`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id     int
		vector []float32
	}{{1, []float32{1, 0, 0}}, {2, []float32{0.8, 0.2, 0}}, {3, []float32{0, 1, 0}}}
	for _, row := range rows {
		vector, err := Serialize(row.vector, dimensions)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO vec_posts(record_id, embedding) VALUES (?, ?)`, row.id, vector); err != nil {
			t.Fatal(err)
		}
	}
	query, _ := Serialize([]float32{1, 0, 0}, dimensions)
	found, err := database.Query(`SELECT record_id, distance FROM vec_posts WHERE embedding MATCH ? AND k = 3 ORDER BY distance`, query)
	if err != nil {
		t.Fatal(err)
	}
	defer found.Close()
	var identities []int
	var distances []float64
	for found.Next() {
		var identity int
		var distance float64
		if err := found.Scan(&identity, &distance); err != nil {
			t.Fatal(err)
		}
		identities = append(identities, identity)
		distances = append(distances, distance)
	}
	if err := found.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(identities, []int{1, 2, 3}) {
		t.Fatalf("identities=%v distances=%v", identities, distances)
	}
	if distances[0] != 0 || !(distances[0] < distances[1] && distances[1] < distances[2]) {
		t.Fatal(fmt.Sprintf("unexpected cosine distances %v", distances))
	}
}
