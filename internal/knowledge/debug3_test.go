package knowledge

import (
	"math"
	"math/rand"
	"testing"
)

func TestHNSWIndex_DebugMatch(t *testing.T) {
	dim := 128
	idx := NewHNSWIndex(dim)
	idx.SetEfSearch(128)
	idx.efConstruction = 200

	rng := rand.New(rand.NewSource(42))
	n := 500
	vecs := make([][]float64, n)
	for i := 0; i < n; i++ {
		v := make([]float64, dim)
		for j := range v {
			v[j] = rng.NormFloat64()
		}
		var norm float64
		for _, x := range v {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		for j := range v {
			v[j] /= norm
		}
		vecs[i] = v
		id := string(rune('A'+i%26)) + string(rune('a'+i/26))
		idx.Add(id, v)
	}

	misses := 0
	for trial := 0; trial < 10; trial++ {
		qi := rng.Intn(n)
		query := vecs[qi]
		qID := string(rune('A'+qi%26)) + string(rune('a'+qi/26))

		hits := idx.Search(query, 20)
		found := false
		for _, h := range hits {
			if h.ID == qID {
				found = true
				t.Logf("trial %d: qi=%d id=%s found score=%.6f", trial, qi, qID, h.Score)
				break
			}
		}
		if !found {
			misses++
			t.Logf("trial %d: qi=%d id=%s NOT FOUND. top-3:", trial, qi, qID)
			for i := 0; i < 3 && i < len(hits); i++ {
				t.Logf("  [%d] %s: %.6f", i, hits[i].ID, hits[i].Score)
			}
		}
	}
	if misses > 1 {
		t.Errorf("%d/10 misses", misses)
	}
}
