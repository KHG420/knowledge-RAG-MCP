// Command eval reads .searchlog.jsonl and human-annotation JSON to compute
// ranking metrics: NDCG@5, MRR, and Recall@10.
//
// Usage:
//
//	go run scripts/eval.go \
//	    -searchlog /path/to/.searchlog.jsonl \
//	    -annotations /path/to/annotations.json
//
// Annotation format (JSON object mapping query text → relevant chunk IDs):
//
//	{
//	    "what is RAG?": ["chunk-001", "chunk-042"],
//	    "BM25 scoring": ["chunk-103"]
//	}
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"time"
)

// SearchLogEntry mirrors internal/knowledge.SearchLogEntry for standalone eval.
type SearchLogEntry struct {
	Query      string        `json:"query"`
	HitCount   int           `json:"hit_count"`
	HitIDs     []string      `json:"hit_ids,omitempty"`
	TopScores  []float64     `json:"top_scores,omitempty"`
	JudgedHits []string      `json:"judged_hits,omitempty"`
	Filter     *SearchFilter `json:"filter,omitempty"`
	Timestamp  time.Time     `json:"timestamp"`
}

// SearchFilter mirrors a minimal subset for JSON decoding.
type SearchFilter struct {
	SourceType string `json:"source_type,omitempty"`
	DocSlug    string `json:"doc_slug,omitempty"`
	Section    string `json:"section,omitempty"`
	Coarse     bool   `json:"coarse,omitempty"`
}

// queryMetrics holds per-query evaluation results.
type queryMetrics struct {
	Query     string
	Hits      int      // total hits returned
	Relevant  int      // total judged relevant for this query
	ReciprocalRank float64 // 1 / rank of first relevant hit (0 if none)
	NDCG5     float64
	Recall10  float64
}

func main() {
	searchlogPath := flag.String("searchlog", ".searchlog.jsonl", "path to .searchlog.jsonl")
	annotationsPath := flag.String("annotations", "annotations.json", "path to annotation JSON")
	flag.Parse()

	// Load annotations.
	annotations, err := loadAnnotations(*annotationsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: loading annotations: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d annotated queries from %s\n\n", len(annotations), *annotationsPath)

	// Load search log entries.
	entries, err := loadSearchLog(*searchlogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: loading search log: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d search log entries from %s\n\n", len(entries), *searchlogPath)

	// Evaluate.
	var metrics []queryMetrics
	var totalNDCG5, totalMRR, totalRecall10 float64
	count := 0

	for _, entry := range entries {
		judged, ok := annotations[entry.Query]
		if !ok {
			continue // skip unannotated queries
		}
		judgedSet := makeSet(judged)

		m := queryMetrics{
			Query:    entry.Query,
			Hits:     entry.HitCount,
			Relevant: len(judged),
		}

		// MRR: first relevant hit rank.
		m.ReciprocalRank = computeRR(entry.HitIDs, judgedSet)

		// NDCG@5.
		m.NDCG5 = computeNDCG(entry.HitIDs, judgedSet, 5)

		// Recall@10.
		m.Recall10 = computeRecall(entry.HitIDs, judgedSet, 10)

		metrics = append(metrics, m)
		totalNDCG5 += m.NDCG5
		totalMRR += m.ReciprocalRank
		totalRecall10 += m.Recall10
		count++
	}

	if count == 0 {
		fmt.Println("No annotated queries matched search log entries.")
		return
	}

	// Print per-query detail.
	fmt.Printf("%-40s %5s %8s %8s %8s\n", "Query", "Hits", "RR", "NDCG@5", "Recall@10")
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	for _, m := range metrics {
		short := m.Query
		if len(short) > 38 {
			short = short[:35] + "..."
		}
		fmt.Printf("%-40s %5d %8.4f %8.4f %8.4f\n",
			short, m.Hits, m.ReciprocalRank, m.NDCG5, m.Recall10)
	}

	// Print summary.
	fmt.Println()
	fmt.Printf("MRR       (avg over %d queries): %.4f\n", count, totalMRR/float64(count))
	fmt.Printf("NDCG@5    (avg over %d queries): %.4f\n", count, totalNDCG5/float64(count))
	fmt.Printf("Recall@10 (avg over %d queries): %.4f\n", count, totalRecall10/float64(count))
}

// loadAnnotations reads the annotation JSON file.
func loadAnnotations(path string) (map[string][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var annotations map[string][]string
	if err := json.NewDecoder(f).Decode(&annotations); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	return annotations, nil
}

// loadSearchLog reads the JSONL search log file.
func loadSearchLog(path string) ([]SearchLogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []SearchLogEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry SearchLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("decoding line: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return entries, nil
}

// computeRR returns the reciprocal rank of the first relevant hit (0 if none).
func computeRR(hitIDs []string, judgedSet map[string]bool) float64 {
	for i, id := range hitIDs {
		if judgedSet[id] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// computeNDCG computes NDCG at k.
func computeNDCG(hitIDs []string, judgedSet map[string]bool, k int) float64 {
	if k <= 0 || len(hitIDs) == 0 {
		return 0
	}
	if len(hitIDs) > k {
		hitIDs = hitIDs[:k]
	}

	// DCG: relevance = 1 if judged, 0 otherwise.
	dcg := 0.0
	for i, id := range hitIDs {
		rel := 0.0
		if judgedSet[id] {
			rel = 1.0
		}
		if i == 0 {
			dcg += rel // rank 1: log2(2) = 1
		} else {
			dcg += rel / math.Log2(float64(i+2)) // rank i+1: log2(i+2)
		}
	}

	// IDCG: ideal DCG — all relevant at top.
	idealCount := 0
	for _, id := range hitIDs {
		if judgedSet[id] {
			idealCount++
		}
	}
	idcg := 0.0
	for i := 0; i < idealCount && i < k; i++ {
		if i == 0 {
			idcg += 1.0
		} else {
			idcg += 1.0 / math.Log2(float64(i+2))
		}
	}

	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// computeRecall computes Recall at k: fraction of relevant items found in top-k.
func computeRecall(hitIDs []string, judgedSet map[string]bool, k int) float64 {
	if len(judgedSet) == 0 {
		return 0
	}
	if k > 0 && len(hitIDs) > k {
		hitIDs = hitIDs[:k]
	}
	found := 0
	for _, id := range hitIDs {
		if judgedSet[id] {
			found++
		}
	}
	return float64(found) / float64(len(judgedSet))
}

// makeSet converts a string slice to a set for O(1) lookup.
func makeSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

