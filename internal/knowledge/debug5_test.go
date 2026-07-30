package knowledge

import (
    "fmt"
    "math"
    "math/rand"
    "testing"
)

func TestHNSWIndex_Debug5(t *testing.T) {
    dim := 128
    idx := NewHNSWIndex(dim)
    idx.SetEfSearch(500) // very high
    idx.efConstruction = 500

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

    // Query v237 specifically
    qi := 237
    hits := idx.Search(vecs[qi], 10)
    found := false
    for _, h := range hits {
        if h.ID == fmt.Sprintf("v%d", qi) {
            found = true
            t.Logf("qi=%d FOUND score=%.6f", qi, h.Score)
        }
    }
    if !found {
        t.Logf("qi=%d NOT FOUND. Top hits:", qi)
        for i, h := range hits {
            t.Logf("  [%d] %s: %.6f", i, h.ID, h.Score)
        }
        // Check if the node exists
        if n, ok := idx.nodes[fmt.Sprintf("v%d", qi)]; ok {
            score := cosineSimilarity(vecs[qi], n.vec)
            t.Logf("  direct cosine with self: %.6f", score)
        }
    }
}
