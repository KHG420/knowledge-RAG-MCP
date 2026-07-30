package knowledge

import (
    "fmt"
    "math"
    "math/rand"
    "testing"
)

func TestHNSWIndex_Debug7(t *testing.T) {
    dim := 128
    idx := NewHNSWIndex(dim)
    idx.efConstruction = 200

    rng := rand.New(rand.NewSource(42))
    total := 500
    for i := 0; i < total; i++ {
        v := make([]float64, dim)
        for j := range v {
            v[j] = rng.NormFloat64()
        }
        var norm float64
        for _, x := range v { norm += x * x }
        norm = math.Sqrt(norm)
        for j := range v { v[j] /= norm }
        id := fmt.Sprintf("v%d", i)
        idx.Add(id, v)
    }

    // Inspect v17
    node17 := idx.nodes["v17"]
    if node17 == nil {
        t.Fatal("v17 not in index")
    }
    t.Logf("v17: layers=%d", len(node17.layers))
    for lc := range node17.layers {
        t.Logf("  layer %d: %d neighbors: %v", lc, len(node17.layers[lc]), node17.layers[lc])
    }

    // Check first 5 nodes
    for i := 0; i < 5; i++ {
        id := fmt.Sprintf("v%d", i)
        nd := idx.nodes[id]
        if nd != nil {
            t.Logf("%s: layers=%d, layer0_neighbors=%v", id, len(nd.layers), nd.layers[0])
        }
    }
}
