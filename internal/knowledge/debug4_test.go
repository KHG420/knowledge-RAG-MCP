package knowledge

import (
    "fmt"
    "math"
    "math/rand"
    "testing"
)

func TestHNSWIndex_Debug4(t *testing.T) {
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

    misses := 0
    for trial := 0; trial < 10; trial++ {
        qi := rng.Intn(n)
        query := vecs[qi]
        qID := fmt.Sprintf("v%d", qi)

        hits := idx.Search(query, 20)
        found := false
        for _, h := range hits {
            if h.ID == qID {
                found = true
                break
            }
        }
        if !found {
            misses++
            t.Logf("trial %d: qi=%d NOT FOUND. top: %s=%.4f", trial, qi, hits[0].ID, hits[0].Score)
        }
    }
    t.Logf("misses: %d/10", misses)
}
