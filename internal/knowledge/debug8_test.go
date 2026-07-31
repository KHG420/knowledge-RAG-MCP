package knowledge

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func TestHNSWIndex_Debug8(t *testing.T) {
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
		for _, x := range v {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		for j := range v {
			v[j] /= norm
		}
		id := fmt.Sprintf("v%d", i)
		idx.Add(id, v)
	}

	t.Logf("entry=%s maxLevel=%d", idx.entryID, idx.maxLevel)

	// Check nodes 280-290
	for i := 280; i < 290; i++ {
		id := fmt.Sprintf("v%d", i)
		nd := idx.nodes[id]
		if nd != nil {
			t.Logf("%s: layers=%d, layer0_neighbors=%d", id, len(nd.layers), len(nd.layers[0]))
		} else {
			t.Logf("%s: NOT IN INDEX!", id)
		}
	}
}
