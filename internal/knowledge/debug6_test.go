package knowledge

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func TestHNSWIndex_Debug6(t *testing.T) {
	dim := 128
	idx := NewHNSWIndex(dim)
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
		id := fmt.Sprintf("v%d", i)
		idx.Add(id, v)
	}

	// Check connectivity from entry point at layer 0
	entry := idx.entryID
	visited := map[string]bool{}
	var dfs func(id string)
	dfs = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		node := idx.nodes[id]
		if node == nil || len(node.layers) == 0 {
			return
		}
		for _, nid := range node.layers[0] {
			dfs(nid)
		}
	}
	dfs(entry)

	t.Logf("entry=%s, reachable=%d, total=%d", entry, len(visited), n)
	if len(visited) < n {
		t.Errorf("DISCONNECTED: %d nodes unreachable", n-len(visited))
		// Show some unreachable nodes
		count := 0
		for i := 0; i < n && count < 5; i++ {
			id := fmt.Sprintf("v%d", i)
			if !visited[id] {
				t.Logf("  unreachable: %s (layer_count=%d)", id, len(idx.nodes[id].layers))
				count++
			}
		}
	}
}
