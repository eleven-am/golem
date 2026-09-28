package contract

import (
	"math"
	"testing"
)

func TestRankingContractDefaultsCompatiblyAndRoundTripsBM25(t *testing.T) {
	base := Index{Name: "content", Folding: FoldingDiacritics, Prefix: []uint8{}, Fields: []Field{{ID: "body", Weight: 1}}}
	payload, err := Encode(base)
	if err != nil {
		t.Fatal(err)
	}
	const released = `{"name":"content","fields":[{"id":"body","weight":1}],"folding":"diacritics","prefix":""}`
	if payload != released {
		t.Fatalf("default payload=%s, want released payload=%s", payload, released)
	}
	decoded, err := Decode(released)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Ranking != RankingTermCount || EffectiveRanking(Index{}) != RankingTermCount {
		t.Fatalf("default ranking=%q effective empty=%q", decoded.Ranking, EffectiveRanking(Index{}))
	}
	base.Ranking = RankingBM25
	bm25, err := Encode(base)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = Decode(bm25)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Ranking != RankingBM25 {
		t.Fatalf("BM25 ranking=%q payload=%s", decoded.Ranking, bm25)
	}
	if _, err := Decode(`{"name":"content","fields":[{"id":"body","weight":1}],"folding":"diacritics","prefix":"","ranking":"term-count"}`); err == nil {
		t.Fatal("non-canonical explicit default ranking accepted")
	}
	base.Ranking = "unknown"
	if _, err := Encode(base); err == nil {
		t.Fatal("unknown ranking accepted")
	}
}

func TestWeightsStayWithinThePortableDatabaseRange(t *testing.T) {
	index := Index{Name: "content", Folding: FoldingDiacritics, Fields: []Field{{ID: "body", Weight: 1}}}
	for _, weight := range []float64{MinimumWeight, 1, MaximumWeight} {
		index.Fields[0].Weight = weight
		if _, err := Encode(index); err != nil {
			t.Fatalf("weight %g rejected: %v", weight, err)
		}
	}
	for _, weight := range []float64{math.SmallestNonzeroFloat64, math.Inf(1), math.NaN()} {
		index.Fields[0].Weight = weight
		if _, err := Encode(index); err == nil {
			t.Fatalf("weight %g accepted", weight)
		}
	}
	index.Fields[0].Weight = math.Nextafter(MaximumWeight, math.Inf(1))
	if _, err := Encode(index); err == nil {
		t.Fatalf("weight above %g accepted", MaximumWeight)
	}
	index.Fields = []Field{{ID: "title", Weight: MaximumWeight}, {ID: "body", Weight: MinimumWeight}}
	if _, err := Encode(index); err == nil {
		t.Fatal("unrepresentable relative weights accepted")
	}
}
