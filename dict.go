package main

import (
	"fmt"
	"os"
	"path/filepath"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
)

func runDictMine(store *knowledge.Store) {
	logPath := filepath.Join(store.DataDir(), ".searchlog.jsonl")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Search log not found: %s\n", logPath)
		fmt.Fprintf(os.Stderr, "The log is created automatically after the server runs and handles queries.\n")
		fmt.Fprintf(os.Stderr, "Start the server with: knowledge-mcp serve\n")
		os.Exit(1)
	}

	fmt.Printf("Mining synonyms from: %s\n\n", logPath)
	candidates, err := knowledge.MineSynonymsFromLog(logPath, 0.01, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Mine failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(knowledge.FormatSynonymCandidates(candidates))
	if len(candidates) > 0 {
		fmt.Println("\n👉 Review the candidates above and manually add verified pairs to dictionaries/*.yaml.")
		fmt.Println("   Then restart the server to pick up the updated dictionary.")
	}
}

// runDictGen uses the LLM to batch-extract domain terminology from knowledge
// base chunks and writes a candidate YAML dictionary file for human review.
func runDictGen(cfg *config.Config, store *knowledge.Store) {
	if cfg.DeepSeekAPIKey == "" {
		fmt.Fprintf(os.Stderr, "Error: DEEPSEEK_API_KEY not configured.\n")
		fmt.Fprintf(os.Stderr, "Set it in knowledge-mcp.toml or via the DEEPSEEK_API_KEY environment variable.\n")
		os.Exit(1)
	}

	// Collect chunk texts from the knowledge base.
	kbName := store.KBName()
	if kbName == "" {
		fmt.Fprintf(os.Stderr, "No knowledge base selected. Set default_kb in knowledge-mcp.toml.\n")
		os.Exit(1)
	}

	fmt.Printf("Collecting chunks from KB: %s ...\n", kbName)

	// Use the existing SearchAll to get representative chunks across the KB.
	hits, err := store.SearchAll("", 200) // empty query = get recent/representative chunks
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to collect chunks: %v\n", err)
		os.Exit(1)
	}

	var chunkTexts []string
	for _, h := range hits {
		text := h.Content.Snippet
		if len(text) > 50 {
			chunkTexts = append(chunkTexts, text)
		}
	}

	if len(chunkTexts) == 0 {
		fmt.Fprintf(os.Stderr, "No chunks found in KB %q. Upload documents first.\n", kbName)
		os.Exit(1)
	}

	fmt.Printf("Found %d chunks. Calling LLM to extract domain terms...\n", len(chunkTexts))

	completer := knowledge.NewDeepSeekCompleter(
		cfg.DeepSeekEndpoint,
		cfg.DeepSeekAPIKey,
		cfg.DeepSeekModel,
	)
	results, err := knowledge.GenerateDictionaryFromChunks(completer, chunkTexts, 20, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Dictionary generation failed: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No domain terms extracted. Try with more or different chunks.")
		return
	}

	yaml := knowledge.FormatDictAsYAML(results)

	// Write to dictionaries/ directory next to the config.
	dictDir := filepath.Join(filepath.Dir(findConfigPath()), "dictionaries")
	if err := os.MkdirAll(dictDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create dictionaries dir: %v\n", err)
		os.Exit(1)
	}

	outPath := filepath.Join(dictDir, kbName+"_generated.yaml")
	if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write dictionary: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n✅ Generated %d domain terms → %s\n", len(results), outPath)
	fmt.Println("👉 Review the file, edit/remove entries, then restart the server to apply.")
	fmt.Printf("   cat %s\n", outPath)
}
