package sqlitevec

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

const (
	cosineParityGolden    = "testdata/cosine_parity.golden.json"
	cosineParityTolerance = 1e-6
	cosineDistanceSQL     = "vec1_cos_distance(?1,?2)"
	rankDistanceSQL       = "vec1_cos_distance(embedding,?1)"
)

var expectedZeroBehaviour = map[string]string{"zero-unit": "1", "unit-zero": "1", "zero-zero": "1"}

var rejectedParityPairs = map[string]bool{"small-magnitude": true, "large-magnitude": true, "mixed-magnitude": true}

type cosineParityPair struct {
	Name     string  `json:"name"`
	Distance float64 `json:"distance"`
	Rejected bool    `json:"-"`
}

type cosineParityRanking struct {
	Name     string    `json:"name"`
	Order    []int     `json:"order"`
	Distance []float64 `json:"distance"`
}

type cosineParityFile struct {
	Pairs    []cosineParityPair    `json:"pairs"`
	Rankings []cosineParityRanking `json:"rankings"`
	Zero     map[string]string     `json:"zero"`
}

type namedVectorPair struct {
	name        string
	left, right []float32
}

func parityRandom(seed uint64, dimensions int, scale float32) []float32 {
	source := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	values := make([]float32, dimensions)
	for index := range values {
		values[index] = (source.Float32()*2 - 1) * scale
	}
	return values
}

func scaled(values []float32, factor float32) []float32 {
	result := make([]float32, len(values))
	for index, value := range values {
		result[index] = value * factor
	}
	return result
}

func parityPairs() []namedVectorPair {
	base3 := []float32{0.25, -0.5, 1}
	base768 := parityRandom(768, 768, 1)
	base1024 := parityRandom(1024, 1024, 1)
	return []namedVectorPair{
		{"identical-3", base3, base3},
		{"identical-scaled-3", base3, scaled(base3, 7.5)},
		{"opposite-3", base3, scaled(base3, -1)},
		{"orthogonal-2", []float32{1, 0}, []float32{0, 1}},
		{"orthogonal-4", []float32{1, 1, 0, 0}, []float32{0, 0, 1, -1}},
		{"near-tie-a", []float32{1, 0, 0}, []float32{1, 1e-4, 0}},
		{"near-tie-b", []float32{1, 0, 0}, []float32{1, 0, 1.0001e-4}},
		{"diagonal-45", []float32{1, 0}, []float32{1, 1}},
		{"small-magnitude", scaled(base3, 1e-18), scaled([]float32{1, 0.5, -0.25}, 1e-18)},
		{"large-magnitude", scaled(base3, 1e15), scaled([]float32{1, 0.5, -0.25}, 1e15)},
		{"mixed-magnitude", scaled(base3, 1e-10), scaled([]float32{1, 0.5, -0.25}, 1e10)},
		{"random-768", base768, parityRandom(7680, 768, 1)},
		{"perturbed-768", base768, func() []float32 {
			noise := parityRandom(7681, 768, 1e-3)
			result := make([]float32, 768)
			for index := range result {
				result[index] = base768[index] + noise[index]
			}
			return result
		}()},
		{"random-1024", base1024, parityRandom(10240, 1024, 1)},
		{"opposite-1024", base1024, scaled(base1024, -1)},
		{"identical-1024", base1024, base1024},
	}
}

type parityCorpus struct {
	name       string
	dimensions int
	vectors    [][]float32
	query      []float32
	limit      int
}

func parityCorpora() []parityCorpus {
	clustered := make([][]float32, 0, 48)
	for index := 0; index < 48; index++ {
		clustered = append(clustered, parityRandom(uint64(100+index), 32, 1))
	}
	query := parityRandom(99, 32, 1)
	for index := 0; index < 6; index++ {
		near := make([]float32, 32)
		noise := parityRandom(uint64(500+index), 32, 1e-3)
		for position := range near {
			near[position] = query[position] + noise[position]
		}
		clustered = append(clustered, near)
	}
	clustered = append(clustered, scaled(query, 3), scaled(query, -2))
	wide := make([][]float32, 0, 40)
	for index := 0; index < 40; index++ {
		wide = append(wide, parityRandom(uint64(2000+index), 1024, 1))
	}
	return []parityCorpus{
		{name: "clustered-32", dimensions: 32, vectors: clustered, query: query, limit: 20},
		{name: "wide-1024", dimensions: 1024, vectors: wide, query: parityRandom(1999, 1024, 1), limit: 40},
	}
}

func parityBlob(t *testing.T, values []float32) []byte {
	t.Helper()
	encoded, err := Serialize(values, len(values))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func rawFloat32Blob(t *testing.T, values []float32) []byte {
	t.Helper()
	encoded, err := binary.Append(nil, binary.LittleEndian, values)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func observeZero(ctx context.Context, database *sql.DB, left, right []byte) string {
	var distance sql.NullFloat64
	if err := database.QueryRowContext(ctx, "SELECT "+cosineDistanceSQL, left, right).Scan(&distance); err != nil {
		return "error"
	}
	if !distance.Valid {
		return "null"
	}
	return strconv.FormatFloat(distance.Float64, 'g', -1, 64)
}

func openParityDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "parity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := Probe(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func observeCosineParity(t *testing.T) cosineParityFile {
	t.Helper()
	ctx := context.Background()
	database := openParityDatabase(t)
	var observed cosineParityFile
	for _, pair := range parityPairs() {
		left, leftErr := Serialize(pair.left, len(pair.left))
		right, rightErr := Serialize(pair.right, len(pair.right))
		if leftErr != nil || rightErr != nil {
			observed.Pairs = append(observed.Pairs, cosineParityPair{Name: pair.name, Rejected: true})
			continue
		}
		var distance float64
		if err := database.QueryRowContext(ctx, "SELECT "+cosineDistanceSQL, left, right).Scan(&distance); err != nil {
			t.Fatalf("%s: %v", pair.name, err)
		}
		observed.Pairs = append(observed.Pairs, cosineParityPair{Name: pair.name, Distance: distance})
	}
	for _, corpus := range parityCorpora() {
		if _, err := database.ExecContext(ctx, "CREATE TABLE corpus (id INTEGER PRIMARY KEY, embedding BLOB NOT NULL)"); err != nil {
			t.Fatal(err)
		}
		for index, vector := range corpus.vectors {
			if _, err := database.ExecContext(ctx, "INSERT INTO corpus (id, embedding) VALUES (?, ?)", index+1, parityBlob(t, vector)); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := database.QueryContext(ctx, "SELECT id, "+rankDistanceSQL+" AS distance FROM corpus ORDER BY distance, id LIMIT ?2", parityBlob(t, corpus.query), corpus.limit)
		if err != nil {
			t.Fatalf("%s: %v", corpus.name, err)
		}
		ranking := cosineParityRanking{Name: corpus.name}
		for rows.Next() {
			var identity int
			var distance float64
			if err := rows.Scan(&identity, &distance); err != nil {
				t.Fatal(err)
			}
			ranking.Order = append(ranking.Order, identity)
			ranking.Distance = append(ranking.Distance, distance)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		observed.Rankings = append(observed.Rankings, ranking)
		if _, err := database.ExecContext(ctx, "DROP TABLE corpus"); err != nil {
			t.Fatal(err)
		}
	}
	zero := rawFloat32Blob(t, []float32{0, 0, 0})
	unit := rawFloat32Blob(t, []float32{1, 0, 0})
	observed.Zero = map[string]string{
		"zero-unit": observeZero(ctx, database, zero, unit),
		"unit-zero": observeZero(ctx, database, unit, zero),
		"zero-zero": observeZero(ctx, database, zero, zero),
	}
	return observed
}

func tieGroups(order []int, distances []float64) []map[int]bool {
	tie := float64(math.Nextafter32(1, 2) - 1)
	var groups []map[int]bool
	for position, identity := range order {
		if position == 0 || distances[position]-distances[position-1] >= tie {
			groups = append(groups, map[int]bool{})
		}
		groups[len(groups)-1][identity] = true
	}
	return groups
}

func sameTieGroups(golden cosineParityRanking, order []int) bool {
	if len(order) != len(golden.Order) {
		return false
	}
	position := 0
	for _, group := range tieGroups(golden.Order, golden.Distance) {
		for range group {
			if !group[order[position]] {
				return false
			}
			position++
		}
	}
	return true
}

func TestCosineDistanceParityGolden(t *testing.T) {
	observed := observeCosineParity(t)
	raw, err := os.ReadFile(cosineParityGolden)
	if err != nil {
		t.Fatal(err)
	}
	var expected cosineParityFile
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	if len(observed.Pairs) != len(expected.Pairs) {
		t.Fatalf("pair count %d, golden %d", len(observed.Pairs), len(expected.Pairs))
	}
	for index, pair := range expected.Pairs {
		got := observed.Pairs[index]
		if got.Name != pair.Name {
			t.Fatalf("pair %d is %s, golden %s", index, got.Name, pair.Name)
		}
		if got.Rejected != rejectedParityPairs[pair.Name] {
			t.Errorf("pair %s rejected=%v, want %v", pair.Name, got.Rejected, rejectedParityPairs[pair.Name])
			continue
		}
		if !got.Rejected && math.Abs(got.Distance-pair.Distance) > cosineParityTolerance {
			t.Errorf("pair %s distance %.12g, golden %.12g", pair.Name, got.Distance, pair.Distance)
		}
	}
	if len(observed.Rankings) != len(expected.Rankings) {
		t.Fatalf("ranking count %d, golden %d", len(observed.Rankings), len(expected.Rankings))
	}
	for index, ranking := range expected.Rankings {
		got := observed.Rankings[index]
		if got.Name != ranking.Name || !sameTieGroups(ranking, got.Order) {
			t.Errorf("ranking %s order %v, golden %v", ranking.Name, got.Order, ranking.Order)
			continue
		}
		for position, distance := range ranking.Distance {
			if math.Abs(got.Distance[position]-distance) > cosineParityTolerance {
				t.Errorf("ranking %s position %d distance %.12g, golden %.12g", ranking.Name, position, got.Distance[position], distance)
			}
		}
	}
	if !reflect.DeepEqual(observed.Zero, expectedZeroBehaviour) {
		t.Errorf("zero-vector behaviour %v, want %v (golden sqlite-vec %v)", observed.Zero, expectedZeroBehaviour, expected.Zero)
	}
}

func TestSerializeRejectsVectorsOutsideVec1NormRange(t *testing.T) {
	direction := []float32{0.6, 0.8}
	for _, norm := range []float32{0, 1e-10, 9e-10, 1.1e9, 1e10} {
		if _, err := Serialize(scaled(direction, norm), 2); err == nil {
			t.Errorf("norm %g accepted", norm)
		}
	}
}

func TestSerializeNormBoundsKeepVec1Exact(t *testing.T) {
	ctx := context.Background()
	database := openParityDatabase(t)
	left, right := []float32{0.6, 0.8}, []float32{0.8, -0.6}
	diagonal := []float32{float32(math.Sqrt2 / 2), float32(math.Sqrt2 / 2)}
	for _, norms := range [][2]float32{{1.001e-9, 1.001e-9}, {0.999e9, 0.999e9}, {1.001e-9, 0.999e9}, {0.999e9, 1.001e-9}, {1, 1}} {
		for _, pair := range []struct {
			right []float32
			want  float64
		}{{left, 0}, {right, 1}, {scaled(left, -1), 2}, {diagonal, 1 - (0.6+0.8)*math.Sqrt2/2}} {
			a, err := Serialize(scaled(left, norms[0]), 2)
			if err != nil {
				t.Fatalf("norms %v: %v", norms, err)
			}
			b, err := Serialize(scaled(pair.right, norms[1]), 2)
			if err != nil {
				t.Fatalf("norms %v: %v", norms, err)
			}
			var distance float64
			if err := database.QueryRowContext(ctx, "SELECT "+cosineDistanceSQL, a, b).Scan(&distance); err != nil {
				t.Fatal(err)
			}
			if math.Abs(distance-pair.want) > cosineParityTolerance {
				t.Errorf("norms %v distance %.12g, want %.12g", norms, distance, pair.want)
			}
		}
	}
}
