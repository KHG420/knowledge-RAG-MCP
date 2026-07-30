package knowledge

import (
    "fmt"
    "math"
    "math/rand"
    "testing"
)

func TestHNSWIndex_Debug500(t *testing.T) {
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
        for _, x := range v { norm += x * x }
        norm = math.Sqrt(norm)
        for j := range v { v[j] /= norm }
        vecs[i] = v
        id := fmt.Sprintf("v%d", i)
        idx.Add(id, v)
    }

    t.Logf("index: %d nodes, entry=%s, maxLevel=%d", idx.Len(), idx.entryID, idx.maxLevel)

    // Query with v0
    hits := idx.Search(vecs[0], 20)
    t.Logf("search(v0) returned %d hits", len(hits))
    if len(hits) > 0 {
        t.Logf("  top: %s score=%.6f", hits[0].ID, hits[0].Score)
        found := false
        for _, h := range hits {
            if h.ID == "v0" { found = true; break }
        }
        if !found {
            t.Error("v0 not in top 20!")
            // Show top 3
            for i := 0; i < 3 && i < len(hits); i++ {
                t.Logf("  [%d] %s: %.6f", i, hits[i].ID, hits[i].Score)
            }
        }
    } else {
        t.Error("no hits at all!")
    }
}
