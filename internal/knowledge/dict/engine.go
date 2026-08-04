// Package dict manages synonym dictionaries, query expansion terms, and
// query rewriting (synonym + LLM). Extracted from Store (REFACTOR_PLAN
// Phase 3.5).
//
// Engine implements knowledge.DictService.
package dict

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// Engine manages dictionary loading, generation, mining, and query rewriting.
// It holds the rewriter references and related terms used by the search engine
// for query expansion.
type Engine struct {
	// ── Rewriters (shared with search.Engine) ──
	rewriter        knowledge.QueryRewriter
	synonymRewriter *knowledge.SynonymRewriter
	llmRewriter     *knowledge.LLMQueryRewriter

	// ── Dictionary terms ──
	dictRelatedTerms []string

	// ── Dict generation / mining dependencies (Phase 3.5 completion) ──
	dataDir       string                   // data directory for search log (.searchlog.jsonl)
	completer     knowledge.TextCompleter  // LLM for GenerateDictionary / RunDictGen
	chunkProvider func() ([]string, error) // provides chunk texts for dictionary generation

	// ── Infrastructure ──
	logger *logging.Logger
	mu     *sync.Mutex
}

// New creates a Dict Engine.
func New(mu *sync.Mutex, logger *logging.Logger) *Engine {
	return &Engine{
		logger: logger,
		mu:     mu,
	}
}

// ── Accessors ────────────────────────────────────────────────────────────────

func (e *Engine) Rewriter() knowledge.QueryRewriter               { return e.rewriter }
func (e *Engine) SetRewriter(rw knowledge.QueryRewriter)          { e.rewriter = rw }
func (e *Engine) SynonymRewriter() *knowledge.SynonymRewriter      { return e.synonymRewriter }
func (e *Engine) SetSynonymRewriter(rw *knowledge.SynonymRewriter) { e.synonymRewriter = rw }
func (e *Engine) LLMRewriter() *knowledge.LLMQueryRewriter         { return e.llmRewriter }
func (e *Engine) SetLLMRewriter(rw *knowledge.LLMQueryRewriter)    { e.llmRewriter = rw }
func (e *Engine) RelatedTerms() []string                           { return e.dictRelatedTerms }
func (e *Engine) SetRelatedTerms(terms []string)                   { e.dictRelatedTerms = terms }
func (e *Engine) Logger() *logging.Logger                          { return e.logger }
func (e *Engine) SetLogger(l *logging.Logger)                      { e.logger = l }
func (e *Engine) Mutex() *sync.Mutex                               { return e.mu }

// ── Dependency setters (Phase 3.5) ─────────────────────────────────────────────

// SetDataDir sets the data directory for search log access (RunDictMine).
func (e *Engine) SetDataDir(dir string) { e.dataDir = dir }

// SetCompleter sets the LLM completer for dictionary generation (GenerateDictionary / RunDictGen).
func (e *Engine) SetCompleter(c knowledge.TextCompleter) { e.completer = c }

// SetChunkProvider sets a function that returns chunk texts for dictionary generation.
// This decouples the dict engine from the knowledge base storage layer.
func (e *Engine) SetChunkProvider(fn func() ([]string, error)) { e.chunkProvider = fn }

// ── DictService interface implementation ──────────────────────────────────────

// LoadDictionaries walks a directory of YAML dictionary files, parses them,
// and populates the synonym rewriter and related-terms list.
func (e *Engine) LoadDictionaries(dir string) error {
	entries, err := knowledge.LoadDictionaries(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	// Populate synonym rewriter.
	syns := knowledge.DictToSynonyms(entries)
	if e.synonymRewriter != nil {
		for term, synonyms := range syns {
			for _, syn := range synonyms {
				e.synonymRewriter.AddSynonym(term, syn)
			}
		}
	}

	// Populate related terms.
	e.dictRelatedTerms = knowledge.DictToRelatedTerms(entries)

	if e.logger != nil {
		e.logger.Infof("dictionaries: loaded %d terms from %s", len(entries), dir)
	}
	return nil
}

// GenerateDictionary uses an LLM completer to extract domain terminology from
// chunk texts and writes a candidate YAML dictionary to dir for human review.
//
// Requires: SetCompleter() and SetChunkProvider() to have been called.
func (e *Engine) GenerateDictionary(dir string) error {
	if e.completer == nil {
		return fmt.Errorf("dict: GenerateDictionary: completer not set — call SetCompleter first")
	}
	if e.chunkProvider == nil {
		return fmt.Errorf("dict: GenerateDictionary: chunk provider not set — call SetChunkProvider first")
	}

	chunkTexts, err := e.chunkProvider()
	if err != nil {
		return fmt.Errorf("dict: get chunk texts: %w", err)
	}
	if len(chunkTexts) == 0 {
		if e.logger != nil {
			e.logger.Warnf("dict: GenerateDictionary: no chunk texts available")
		}
		return nil
	}

	results, err := knowledge.GenerateDictionaryFromChunks(e.completer, chunkTexts, 20, 50)
	if err != nil {
		return fmt.Errorf("dict: GenerateDictionary: %w", err)
	}
	if len(results) == 0 {
		if e.logger != nil {
			e.logger.Infof("dict: GenerateDictionary: no domain terms extracted")
		}
		return nil
	}

	yaml := knowledge.FormatDictAsYAML(results)

	// Write to dir.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("dict: create output dir: %w", err)
	}
	outPath := filepath.Join(dir, "generated.yaml")
	if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
		return fmt.Errorf("dict: write dictionary: %w", err)
	}

	if e.logger != nil {
		e.logger.Infof("dict: generated %d domain terms → %s", len(results), outPath)
	}
	return nil
}

// RunDictMine reads the search log from the data directory and discovers
// candidate synonym pairs via co-occurrence analysis in the search log.
//
// Requires: SetDataDir() to have been called.
func (e *Engine) RunDictMine(configPath string) error {
	if e.dataDir == "" {
		return fmt.Errorf("dict: RunDictMine: data dir not set — call SetDataDir first")
	}

	logPath := filepath.Join(e.dataDir, ".searchlog.jsonl")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return fmt.Errorf("dict: search log not found: %s (run the server first to generate queries)", logPath)
	}

	candidates, err := knowledge.MineSynonymsFromLog(logPath, 0.01, 50)
	if err != nil {
		return fmt.Errorf("dict: synonym mining failed: %w", err)
	}

	if e.logger != nil {
		e.logger.Infof("dict: mined %d synonym candidates from %s", len(candidates), logPath)
	}

	// Print results (the CLI caller will display them).
	fmt.Print(knowledge.FormatSynonymCandidates(candidates))
	if len(candidates) > 0 {
		fmt.Println("\n👉 Review the candidates above and manually add verified pairs to dictionaries/*.yaml.")
		fmt.Println("   Then restart the server to pick up the updated dictionary.")
	}
	return nil
}

// RunDictGen uses the LLM completer and chunk provider to generate a domain
// dictionary from knowledge base documents and writes the candidate YAML to
// the dictionaries directory.
//
// Requires: SetCompleter() and SetChunkProvider() to have been called.
func (e *Engine) RunDictGen(configPath string) error {
	if e.completer == nil {
		return fmt.Errorf("dict: RunDictGen: completer not set — call SetCompleter first")
	}
	if e.chunkProvider == nil {
		return fmt.Errorf("dict: RunDictGen: chunk provider not set — call SetChunkProvider first")
	}

	chunkTexts, err := e.chunkProvider()
	if err != nil {
		return fmt.Errorf("dict: get chunk texts: %w", err)
	}
	if len(chunkTexts) == 0 {
		return fmt.Errorf("dict: no chunks found in KB — upload documents first")
	}

	if e.logger != nil {
		e.logger.Infof("dict: RunDictGen: calling LLM with %d chunks", len(chunkTexts))
	}

	results, err := knowledge.GenerateDictionaryFromChunks(e.completer, chunkTexts, 20, 50)
	if err != nil {
		return fmt.Errorf("dict: dictionary generation failed: %w", err)
	}
	if len(results) == 0 {
		if e.logger != nil {
			e.logger.Infof("dict: RunDictGen: no domain terms extracted")
		}
		return nil
	}

	yaml := knowledge.FormatDictAsYAML(results)

	// Determine output directory: use configPath if provided, else fall back to
	// dataDir-relative dictionaries/.
	dictDir := configPath
	if dictDir == "" {
		if e.dataDir != "" {
			dictDir = filepath.Join(filepath.Dir(e.dataDir), "dictionaries")
		} else {
			dictDir = "dictionaries"
		}
	}
	if err := os.MkdirAll(dictDir, 0o755); err != nil {
		return fmt.Errorf("dict: create dictionaries dir: %w", err)
	}

	outPath := filepath.Join(dictDir, "generated.yaml")
	if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
		return fmt.Errorf("dict: write dictionary: %w", err)
	}

	if e.logger != nil {
		e.logger.Infof("dict: generated %d domain terms → %s", len(results), outPath)
	}
	fmt.Printf("\n✅ Generated %d domain terms → %s\n", len(results), outPath)
	fmt.Println("👉 Review the file, edit/remove entries, then restart the server to apply.")
	return nil
}

func (e *Engine) GetSynonymRewriter() *knowledge.SynonymRewriter  { return e.synonymRewriter }
func (e *Engine) GetLLMRewriter() *knowledge.LLMQueryRewriter     { return e.llmRewriter }
func (e *Engine) GetRelatedTerms() []string                       { return e.dictRelatedTerms }

// Ensure knowledge.DictService interface is satisfied.
var _ knowledge.DictService = (*Engine)(nil)
