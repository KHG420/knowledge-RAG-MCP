package knowledge

import (
	"container/heap"
	"encoding/gob"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
)

// VectorIndex is an approximate nearest-neighbour index for dense vectors.
type VectorIndex interface {
	// Search returns the k nearest neighbours for a query vector.
	// Results are sorted by similarity descending (most similar first).
	Search(query []float64, k int) []VectorHit

	// Add inserts or updates a vector in the index.
	Add(id string, vec []float64)

	// Remove deletes a vector from the index.
	Remove(id string)

	// Len returns the number of vectors in the index.
	Len() int

	// AllIDs returns a snapshot of all vector IDs in the index.
	AllIDs() []string
}

// VectorHit is one result from a vector search.
type VectorHit struct {
	ID    string
	Score float64 // cosine similarity
}

// VectorID builds the canonical key used in HNSWIndex nodes from a document
// slug and chunk ID. The separator is "/" — use ParseVectorID to split.
func VectorID(slug, chunkID string) string {
	return slug + "/" + chunkID
}

// ParseVectorID splits a VectorID key back into slug and chunkID.
// The bool is false when the ID does not contain exactly one "/".
func ParseVectorID(id string) (slug, chunkID string, ok bool) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// HNSWIndex implements VectorIndex using the Hierarchical Navigable Small World
// algorithm (Malkov & Yashunin 2018). It provides fast approximate nearest-
// neighbour search for dense vectors.
type HNSWIndex struct {
	mu sync.RWMutex

	// Parameters.
	M              int     // max connections per node per layer (default 16)
	efConstruction int     // search width during insertion (default 200)
	efSearch       int     // search width during query (default 50)
	mL             float64 // level normalisation factor: 1 / ln(M)

	// Graph state.
	nodes    map[string]*hnswNode
	entryID  string // top-layer entry point
	maxLevel int    // highest layer present
	dim      int    // vector dimensionality
}

// hnswNode is one vertex in the HNSW graph.
type hnswNode struct {
	id     string
	vec    []float64  // normalised for cosine → dot-product
	layers [][]string // neighbours per layer; layers[0] is the bottom layer
}

// NewHNSWIndex creates an HNSW index with sensible defaults.
// M controls the tradeoff between recall and memory (8-64, default 16).
// efConstruction controls build-time search width (default 200).
// efSearch controls query-time search width (default 50).
func NewHNSWIndex(dim int) *HNSWIndex {
	M := 48
	return &HNSWIndex{
		M:              M,
		efConstruction: 400,
		efSearch:       100,
		mL:             1.0 / math.Log(float64(M)),
		nodes:          make(map[string]*hnswNode),
		maxLevel:       -1,
		dim:            dim,
	}
}

// SetEfSearch sets the query-time search width. Larger values increase recall at
// the cost of speed. Typical range: 16-512.
func (idx *HNSWIndex) SetEfSearch(ef int) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if ef < 1 {
		ef = 1
	}
	idx.efSearch = ef
}

// Dim returns the expected vector dimensionality.
func (idx *HNSWIndex) Dim() int { return idx.dim }

// ---- public API ----

func (idx *HNSWIndex) Search(query []float64, k int) []VectorHit {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if idx.entryID == "" || k <= 0 || len(query) != idx.dim {
		return nil
	}

	qNorm := idx.normalise(query)

	// Start at the entry point on the top layer.
	ep := idx.nodes[idx.entryID]
	if ep == nil {
		return nil
	}
	curID := idx.entryID
	curDist := idx.dotScore(qNorm, ep.vec)

	// Greedy descent through layers.
	for lc := idx.maxLevel; lc > 0; lc-- {
		cands := idx.searchLayer(qNorm, curID, curDist, 1, lc)
		if len(cands) > 0 {
			curID = cands[0].id
			curDist = cands[0].dist
		}
	}

	// Bottom layer: search with efSearch width.
	ef := idx.efSearch
	if ef < k {
		ef = k
	}
	candidates := idx.searchLayer(qNorm, curID, curDist, ef, 0)

	// Collect top-k.
	limit := k
	if limit > len(candidates) {
		limit = len(candidates)
	}
	hits := make([]VectorHit, limit)
	for i := 0; i < limit; i++ {
		node := idx.nodes[candidates[i].id]
		score := cosineSimilarity(query, node.vec)
		hits[i] = VectorHit{ID: candidates[i].id, Score: score}
	}
	return hits
}

func (idx *HNSWIndex) Add(id string, vec []float64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if len(vec) != idx.dim {
		return
	}

	// If replacing, remove old first.
	if old, ok := idx.nodes[id]; ok {
		idx.removeNode(old)
	}

	normalised := idx.normalise(vec)

	// Random level assignment: level ~ Geometric(1 - exp(-mL)).
	level := int(-math.Log(rand.Float64()) * idx.mL)
	oldMaxLevel := idx.maxLevel
	if level > idx.maxLevel {
		idx.maxLevel = level
	}

	node := &hnswNode{
		id:     id,
		vec:    normalised,
		layers: make([][]string, level+1),
	}

	// First node — just set entry point.
	if idx.entryID == "" {
		idx.nodes[id] = node
		idx.entryID = id
		return
	}

	// Start from entry point, descend to the new node's level.
	ep := idx.nodes[idx.entryID]
	curID := idx.entryID
	curDist := idx.dotScore(normalised, ep.vec)

	// Ensure the entry point has enough layers to reach the new node's level.
	// Without this, searchLayer returns nil for layers the entry does not have,
	// leaving the new node orphaned in those layers and breaking graph connectivity.
	if level >= len(ep.layers) {
		newLayers := make([][]string, level+1)
		copy(newLayers, ep.layers)
		ep.layers = newLayers
	}

	for lc := idx.maxLevel; lc > level; lc-- {
		cands := idx.searchLayer(normalised, curID, curDist, 1, lc)
		if len(cands) > 0 {
			curID = cands[0].id
			curDist = cands[0].dist
		}
	}

	// Insert into each layer from level down to 0.
	for lc := level; lc >= 0; lc-- {
		ef := idx.efConstruction
		cands := idx.searchLayer(normalised, curID, curDist, ef, lc)
		neighbours := idx.selectNeighbours(cands, idx.M)
		node.layers[lc] = make([]string, len(neighbours))
		for i, n := range neighbours {
			node.layers[lc][i] = n.id
		}
		// Add back-links, pruning to M. The neighbour may need its layers
		// extended if it was the entry point and this is a new high layer.
		for _, n := range neighbours {
			other := idx.nodes[n.id]
			if lc >= len(other.layers) {
				newLayers := make([][]string, lc+1)
				copy(newLayers, other.layers)
				other.layers = newLayers
			}
			other.layers[lc] = append(other.layers[lc], id)
			idx.pruneConnections(other, lc)
		}
		if len(cands) > 0 {
			curID = cands[0].id
			curDist = cands[0].dist
		}
	}

	idx.nodes[id] = node

	// New top-level entry point.
	if level > oldMaxLevel {
		idx.entryID = id
	}
}

func (idx *HNSWIndex) Remove(id string) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	node, ok := idx.nodes[id]
	if !ok {
		return false
	}
	idx.removeNode(node)
	return true
}

func (idx *HNSWIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.nodes)
}

// AllIDs returns a snapshot of all node IDs. Safe under concurrent use.
func (idx *HNSWIndex) AllIDs() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.allIDs()
}

// allIDs returns a snapshot of all node IDs. Caller must hold the lock.
func (idx *HNSWIndex) allIDs() []string {
	ids := make([]string, 0, len(idx.nodes))
	for id := range idx.nodes {
		ids = append(ids, id)
	}
	return ids
}

// HNSWIndexStats holds read-only metadata and statistics about the HNSW index.
type HNSWIndexStats struct {
	NodeCount       int     `json:"nodeCount"`       // total vectors in the index
	Dim             int     `json:"dim"`             // vector dimensionality
	M               int     `json:"m"`               // max connections per node per layer
	EfConstruction  int     `json:"efConstruction"`  // build-time search width
	EfSearch        int     `json:"efSearch"`        // query-time search width
	MaxLevel        int     `json:"maxLevel"`        // highest layer present (0-indexed)
	EntryID         string  `json:"entryId"`         // top-layer entry-point node
	LayerNodeCounts []int   `json:"layerNodeCounts"` // nodes per layer (index = layer)
	TotalEdges      int     `json:"totalEdges"`      // total neighbour links across all layers
	AvgDegree       float64 `json:"avgDegree"`       // average connections per node at layer 0
}

// Stats returns a snapshot of the index metadata and statistics.
func (idx *HNSWIndex) Stats() *HNSWIndexStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	s := &HNSWIndexStats{
		NodeCount:      len(idx.nodes),
		Dim:            idx.dim,
		M:              idx.M,
		EfConstruction: idx.efConstruction,
		EfSearch:       idx.efSearch,
		MaxLevel:       idx.maxLevel,
		EntryID:        idx.entryID,
	}

	// Count nodes per layer.
	if idx.maxLevel >= 0 {
		s.LayerNodeCounts = make([]int, idx.maxLevel+1)
		for _, node := range idx.nodes {
			for lc := range node.layers {
				if lc < len(s.LayerNodeCounts) {
					s.LayerNodeCounts[lc]++
				}
			}
		}
	}

	// Count total edges and compute average degree at layer 0.
	layer0DegreeSum := 0
	layer0NodeCount := 0
	for _, node := range idx.nodes {
		for lc, neighs := range node.layers {
			s.TotalEdges += len(neighs)
			if lc == 0 {
				layer0DegreeSum += len(neighs)
				layer0NodeCount++
			}
		}
	}
	if layer0NodeCount > 0 {
		s.AvgDegree = float64(layer0DegreeSum) / float64(layer0NodeCount)
	}

	return s
}

// ---- internal helpers ----

// distanceNode is used in the priority queue during layer search.
type distNode struct {
	id   string
	dist float64 // higher = closer (dot product on normalised vectors)
}

// searchLayer performs a greedy beam search within one layer, returning up to
// ef nearest neighbours. curID/curDist seed the search.
func (idx *HNSWIndex) searchLayer(query []float64, curID string, curDist float64, ef int, level int) []distNode {
	// Validate that the seed node has this layer. When inserting a node at a
	// higher level than any existing node, the seed (old entry point) may not
	// reach this layer — return empty results so no cross-links are created.
	seed := idx.nodes[curID]
	if seed == nil || level >= len(seed.layers) {
		return nil
	}

	visited := map[string]bool{curID: true}

	// Max-heap of candidates: largest dist (most similar) on top — we explore
	// the most promising neighbours first.
	candHeap := &maxDistHeap{}
	heap.Init(candHeap)
	heap.Push(candHeap, distNode{id: curID, dist: curDist})

	// Min-heap of results: smallest dist (least similar) on top — easy eviction.
	resHeap := &minDistHeap{}
	heap.Init(resHeap)
	heap.Push(resHeap, distNode{id: curID, dist: curDist})

	for candHeap.Len() > 0 {
		c := heap.Pop(candHeap).(distNode)
		// Stop when the best remaining candidate is worse than the worst result.
		if resHeap.Len() >= ef && c.dist < (*resHeap)[0].dist {
			break
		}

		node := idx.nodes[c.id]
		if node == nil || level >= len(node.layers) {
			continue
		}
		for _, nid := range node.layers[level] {
			if visited[nid] {
				continue
			}
			visited[nid] = true
			other := idx.nodes[nid]
			if other == nil {
				continue
			}
			d := idx.dotScore(query, other.vec)
			if resHeap.Len() >= ef && d < (*resHeap)[0].dist {
				continue
			}
			heap.Push(candHeap, distNode{id: nid, dist: d})
			heap.Push(resHeap, distNode{id: nid, dist: d})
			if resHeap.Len() > ef {
				heap.Pop(resHeap)
			}
		}
	}

	// Collect results, descending by distance.
	n := resHeap.Len()
	results := make([]distNode, n)
	for i := n - 1; i >= 0; i-- {
		results[i] = heap.Pop(resHeap).(distNode)
	}
	return results
}

// selectNeighbours picks the M nearest nodes from candidates.
func (idx *HNSWIndex) selectNeighbours(candidates []distNode, M int) []distNode {
	if len(candidates) <= M {
		return candidates
	}
	sorted := make([]distNode, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].dist > sorted[j].dist
	})
	return sorted[:M]
}

// pruneConnections trims a node's neighbour list at a given layer. To prevent
// graph fragmentation, pruning is deferred until the neighbour count exceeds
// 2×M, and then only the M nearest neighbours are retained.
func (idx *HNSWIndex) pruneConnections(node *hnswNode, level int) {
	if level >= len(node.layers) {
		return
	}
	neighbors := node.layers[level]
	if len(neighbors) <= idx.M*2 {
		return
	}

	type scored struct {
		id   string
		dist float64
	}
	list := make([]scored, len(neighbors))
	for i, nid := range neighbors {
		other := idx.nodes[nid]
		if other == nil {
			list[i] = scored{id: nid, dist: -math.MaxFloat64}
		} else {
			list[i] = scored{id: nid, dist: idx.dotScore(node.vec, other.vec)}
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].dist > list[j].dist })
	trimmed := make([]string, idx.M)
	for i := 0; i < idx.M; i++ {
		trimmed[i] = list[i].id
	}
	node.layers[level] = trimmed
}

// removeNode removes a node and all its cross-references from the graph.
func (idx *HNSWIndex) removeNode(node *hnswNode) {
	delete(idx.nodes, node.id)

	// Remove node ID from neighbours' lists.
	for _, other := range idx.nodes {
		for lc := range other.layers {
			filtered := other.layers[lc][:0]
			for _, nid := range other.layers[lc] {
				if nid != node.id {
					filtered = append(filtered, nid)
				}
			}
			other.layers[lc] = filtered
		}
	}

	// Reset entry point if we removed it.
	if idx.entryID == node.id {
		idx.entryID = ""
		idx.maxLevel = -1
		// Pick any remaining node as new entry.
		for id, n := range idx.nodes {
			idx.entryID = id
			idx.maxLevel = len(n.layers) - 1
			break
		}
	}
}

// dotScore computes the dot product between two normalised vectors. Range: [-1, 1].
// Because vectors are normalised, dot product is equivalent to cosine similarity.
func (idx *HNSWIndex) dotScore(a, b []float64) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// normalise returns a unit-length copy of vec.
func (idx *HNSWIndex) normalise(vec []float64) []float64 {
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm < 1e-12 {
		// Zero vector: return as-is.
		out := make([]float64, len(vec))
		copy(out, vec)
		return out
	}
	out := make([]float64, len(vec))
	for i, v := range vec {
		out[i] = v / norm
	}
	return out
}

// cosineSimilarity computes cosine similarity between two raw (possibly
// unnormalised) vectors. Used for the final score returned to callers.
// ---- priority queues ----

// minDistHeap is a min-heap (smallest dist on top).
type minDistHeap []distNode

func (h minDistHeap) Len() int           { return len(h) }
func (h minDistHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h minDistHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minDistHeap) Push(x any)        { *h = append(*h, x.(distNode)) }
func (h *minDistHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// maxDistHeap is a max-heap (largest dist on top).
type maxDistHeap []distNode

func (h maxDistHeap) Len() int           { return len(h) }
func (h maxDistHeap) Less(i, j int) bool { return h[i].dist > h[j].dist }
func (h maxDistHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxDistHeap) Push(x any)        { *h = append(*h, x.(distNode)) }
func (h *maxDistHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// ---- persistence (gob encoding) ----

// hnswNodeData is the gob-serialisable form of hnswNode. We flatten the layered
// neighbours into a simple struct because gob handles slices-of-slices of strings.
type hnswNodeData struct {
	ID     string
	Vec    []float64
	Layers [][]string
}

// hnswIndexData is the gob-serialisable form of HNSWIndex.
type hnswIndexData struct {
	M              int
	EfConstruction int
	EfSearch       int
	Dim            int
	EntryID        string
	MaxLevel       int
	Nodes          []hnswNodeData
}

// Save writes the index to a file using gob encoding.
func (idx *HNSWIndex) Save(path string) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	data := hnswIndexData{
		M:              idx.M,
		EfConstruction: idx.efConstruction,
		EfSearch:       idx.efSearch,
		Dim:            idx.dim,
		EntryID:        idx.entryID,
		MaxLevel:       idx.maxLevel,
		Nodes:          make([]hnswNodeData, 0, len(idx.nodes)),
	}
	for _, node := range idx.nodes {
		nd := hnswNodeData{
			ID:     node.id,
			Vec:    node.vec,
			Layers: node.layers,
		}
		data.Nodes = append(data.Nodes, nd)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(data)
}

// LoadHNSWIndex reads an HNSW index from a gob-encoded file. Returns nil if the
// file does not exist.
func LoadHNSWIndex(path string) (*HNSWIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var data hnswIndexData
	if err := gob.NewDecoder(f).Decode(&data); err != nil {
		return nil, err
	}

	idx := &HNSWIndex{
		M:              data.M,
		efConstruction: data.EfConstruction,
		efSearch:       data.EfSearch,
		mL:             1.0 / math.Log(float64(data.M)),
		dim:            data.Dim,
		entryID:        data.EntryID,
		maxLevel:       data.MaxLevel,
		nodes:          make(map[string]*hnswNode, len(data.Nodes)),
	}
	for _, nd := range data.Nodes {
		idx.nodes[nd.ID] = &hnswNode{
			id:     nd.ID,
			vec:    nd.Vec,
			layers: nd.Layers,
		}
	}
	return idx, nil
}
