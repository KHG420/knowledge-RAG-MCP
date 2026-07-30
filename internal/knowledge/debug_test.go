package knowledge

import (
    "fmt"
    "math"
    "math/rand"
    "testing"
)

func TestHNSWIndex_Debug(t *testing.T) {
    dim := 4
    idx := NewHNSWIndex(dim)
    idx.SetEfSearch(50)
    idx.efConstruction = 200

    rng := rand.New(rand.NewSource(42))
    vecs := make([][]float64, 10)
    for i := 0; i < 10; i++ {
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

    t.Logf("index has %d nodes, entry=%s, maxLevel=%d", idx.Len(), idx.entryID, idx.maxLevel)

    // Query with v0
    hits := idx.Search(vecs[0], 10)
    t.Logf("search returned %d hits", len(hits))
    for _, h := range hits {
        t.Logf("  %s: %.6f", h.ID, h.Score)
    }

    found := false
    for _, h := range hits {
        if h.ID == "v0" {
            found = true
        }
    }
    if !found {
        t.Error("v0 not found in results")
    }
}
