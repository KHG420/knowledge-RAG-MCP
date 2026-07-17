# RAG Optimization Roadmap

> Established on 2026-07-13 after auditing the knowledge-mcp project. Based on an item-by-item
> evaluation of 9 advanced RAG techniques, sorted by value/resistance ratio,
> tagged as "Done / Planned / Later / Skip".

## 1. Done ✓ (No Changes Needed)

### Intelligent Chunking (`internal/knowledge/chunker.go`)
- Split at `\n\n` paragraph boundaries → merge short chunks (<200 chars) → split long chunks at sentence boundaries (>2000 chars) → merge fragments (<60 chars)
- ~200 char overlap between chunks (sentence-aligned)
- Markdown heading tracking + section role classification (abstract/introduction/methodology, etc.)
- Embedding cosine similarity merge of adjacent synonymous chunks (threshold 0.75)
- Two-layer structure: Fine chunk + Coarse section chunk

### Parent-Child Chunk Indexing (`internal/knowledge/doc.go:58, store.go:870-930`)
- `ChunkWithMeta.SectionID` → `CHUNKS.toml.SectionChunkID` → `chunks/sections/S00.md`
- `knowledge_read` supports `level: "section"` to read the full parent section of a child chunk
- `context` parameter reads a window of adjacent chunks (0-5 before and after)

### Hierarchical Retrieval (`internal/knowledge/search.go:666-768`)
- `coarseToFineFilter`: BM25-scored section-level chunks → pick top-3 → refine search within matching sections only
- Enabled via: `SearchFilter.Coarse = true`

---

## 2. Planned → Next to Implement

### 1. Metadata Filter Enhancement

**Current state**: `SearchFilter` has only four fields: `SourceType` / `Section` / `DocSlug` / `Coarse`.
No custom tags, no document date filtering in `meta.json`.

**Plan**:

- [x] New fields for `SearchFilter`:
  - `Tags []string` — tag whitelist (document must match at least one tag)
  - `AddedAfter` / `AddedBefore` — upload time range (`meta.json` already has `AddedAt`)
- [x] New `Tags []string` field in `meta.json` structure
- [x] Optional `tags` parameter for `knowledge_upload` tool (comma-separated)
- [x] Optional `tags` / `addedAfter` / `addedBefore` parameters for `knowledge_search` tool
- [x] Tag and time filter logic added to `collectEntries` / `collectEntriesFromCandidates`

### 2. Expose Coarse Mode to MCP Tools

**Current state**: `SearchFilter.Coarse` is only available at the Go code layer; MCP callers cannot trigger it.

**Plan**:

- [x] New `coarse` boolean parameter for `knowledge_search` tool
- [x] When `coarse=true`, handler sets `filter.Coarse = true`

### 3. Automatic Parent Chunk Hit Hint

**Current state**: Multiple chunk hits from the same section are returned individually; callers don't know they belong to the same section.

**Plan**:

- [x] New `SectionHint` field on `SearchHit`: when a section has ≥2 chunk hits,
  returns `"Multiple hits in section 'XXX'. Consider reading with level=section for full context."`
- [x] Implementation: after Phase 9 of `Search` / `HybridSearch`, count sectionChunkID frequency,
  add section hint to chunks from sections with ≥2 hits
- [x] Alternative: make `knowledge_read` description explicitly suggest "if search results come from the same section, use level=section"

---

## 3. Later → Do When Prerequisites Are Met

### 4. Evaluation Framework

**Current state**: `SearchLogger` interface already exists, outputs `.searchlog.jsonl` (Query / HitCount / HitIDs / TopScores / Filter / Timestamp).
`SearchLogEntry` already has a `JudgedHits` field for human annotations. The evaluation script `scripts/eval.go` is complete,
reading `.searchlog.jsonl` + human annotation JSON and computing NDCG@5 / MRR / Recall@10.

**Prerequisite**: Needs accumulated human-annotated query→relevant_chunk mappings first.

**Done**:

- [x] Evaluation script `scripts/eval.go`: read `.searchlog.jsonl` + human annotation JSON → compute NDCG@5 / MRR / Recall@10
- [x] New `JudgedHits []string` field on `SearchLogEntry` (human-annotated relevant chunk IDs)
- [x] An interactive annotation CLI (optional)

---

## 4. Skipped ✗

| Suggestion | Reason for Skipping |
|------------|---------------------|
| **Context Compression** | Requires deploying an additional compression model; the calling agent can already summarize. Adds latency and operational cost with marginal benefit |
| **GraphRAG** | Requires graph index + entity extraction + relationship reasoning, a fundamental overhaul of the current BM25/vector architecture with unsustainable complexity |
| **Agentic RAG** | The MCP caller (Claude/GPT) is itself an agent with routing, reflection, and retry capabilities. Rebuilding an agent layer inside the MCP server is redundant |
| **Prompt Engineering** | `knowledge_search` description already has detailed query rewriting rules; the caller's system prompt is outside this project's control |
