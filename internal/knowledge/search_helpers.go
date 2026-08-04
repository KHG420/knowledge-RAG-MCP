package knowledge

import (
	"container/heap"
	"sort"
	"strings"
)

func deduplicateSnippets(hits []SearchHit) []SearchHit {
	for i := 0; i < len(hits); i++ {
		if hits[i].DuplicateOf != "" {
			continue // already marked
		}
		for j := i + 1; j < len(hits); j++ {
			if hits[j].DuplicateOf != "" {
				continue
			}
			if hits[i].Document.ID != hits[j].Document.ID {
				continue
			}
			if snippetJaccard(hits[i].Content.Snippet, hits[j].Content.Snippet) >= dedupJaccardThreshold {
				// Mark the lower-scoring hit as a duplicate.
				if hits[i].Score >= hits[j].Score {
					hits[j].DuplicateOf = hits[i].Location.ChunkID
				} else {
					hits[i].DuplicateOf = hits[j].Location.ChunkID
					break // hits[i] is now a duplicate; no need to check further for i
				}
			}
		}
	}
	return hits
}

// snippetJaccard computes the Jaccard similarity between two snippet strings
// using word-level tokenisation (strings.Fields). Returns 0 for empty inputs.
func snippetJaccard(a, b string) float64 {
	tokensA := strings.Fields(a)
	tokensB := strings.Fields(b)
	if len(tokensA) == 0 && len(tokensB) == 0 {
		return 0
	}

	setA := make(map[string]bool, len(tokensA))
	for _, t := range tokensA {
		setA[t] = true
	}

	intersection := 0
	setB := make(map[string]bool, len(tokensB))
	for _, t := range tokensB {
		setB[t] = true
		if setA[t] {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// matchesMetaTags returns true when the document passes the tag filter:
// if filterTags is empty, all documents match (no filter applied);
// otherwise the document must have at least one tag that equal-folds to one of
// the filter tags.
func matchesMetaTags(docTags, filterTags []string) bool {
	if len(filterTags) == 0 {
		return true
	}
	for _, dt := range docTags {
		for _, ft := range filterTags {
			if strings.EqualFold(dt, ft) {
				return true
			}
		}
	}
	return false
}

// --- top-K partial sort (min-heap) ---

// topKHeap maintains the k highest scored items using a min-heap.
type topKHeap struct {
	items []rankedEntry
	k     int
}

func (h topKHeap) Len() int           { return len(h.items) }
func (h topKHeap) Less(i, j int) bool { return h.items[i].score < h.items[j].score }
func (h topKHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *topKHeap) Push(x any)        { h.items = append(h.items, x.(rankedEntry)) }
func (h *topKHeap) Pop() any {
	old := h.items
	n := len(old)
	x := old[n-1]
	h.items = old[:n-1]
	return x
}

// partialSortTopK keeps the top k highest-scored items and sorts them
// descending. When len(results) ≤ k it does a full sort; for large
// collections it uses a bounded min-heap to avoid O(n log n) sorting.
func partialSortTopK(results []rankedEntry, k int) []rankedEntry {
	if k <= 0 || len(results) <= k {
		sort.Slice(results, func(i, j int) bool {
			return results[i].score > results[j].score
		})
		return results
	}
	h := &topKHeap{items: make([]rankedEntry, 0, k), k: k}
	heap.Init(h)
	for i := range results {
		if h.Len() < k {
			heap.Push(h, results[i])
		} else if results[i].score > h.items[0].score {
			h.items[0] = results[i]
			heap.Fix(h, 0)
		}
	}
	// Extract in descending order.
	out := make([]rankedEntry, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(h).(rankedEntry)
	}
	return out
}
