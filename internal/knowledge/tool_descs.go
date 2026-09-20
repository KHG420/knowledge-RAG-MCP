package knowledge

// Default MCP tool descriptions. These are the built-in, English+Chinese
// descriptions shown to LLM agents. Custom descriptions can be set via the
// Web UI (PUT /api/tool-descriptions) and take effect after a service restart.
//
// The server registers exactly three MCP tools:
//   - knowledge_research  (search)
//   - knowledge_read      (read one chunk/section)
//   - knowledge_list_kbs  (list knowledge bases)
//
// Descriptions are intentionally concise and domain-neutral. They must not
// promise that semantic embedding or reranking models are configured: those are
// optional server-side settings.

const DefaultSearchDesc = `Search the knowledge base for passages relevant to a question.

Use one call per focused question; decompose broad questions into a few calls. Each returned result carries a score and provenance fields. The score is a ranking signal — not a truth, confidence, or relevance verdict.

OUTPUT
A JSON envelope:
- results: array of ranked passages (may be empty)
- searched_kbs / failed_kbs: which knowledge bases were searched or failed
- warnings: configuration or partial-coverage notes
- coverage: "complete" or "partial" for the SEARCH, not the answer. "complete" means every knowledge base the server selected and attempted was searched successfully; it does NOT mean every knowledge base was searched or that the results answer the question.

Check coverage before concluding that "nothing exists": partial coverage means some selected knowledge bases could not be searched, so useful passages may be missing. An empty results array only means no match was found among the searched knowledge bases.

Runtime success or fallback of optional embedding/reranking models is not reported; do not infer model use from configuration.

FOLLOW-UP
To read a hit, call knowledge_read with docSlug=result.document.id, chunkID=result.location.chunk_id, and kbName=result.kb_name from that same result. When several knowledge bases are searched, use each result's own kb_name rather than reusing an earlier argument.

Retrieval may use keyword matching and, when the server is configured with them, semantic embedding or cross-encoder reranking. Do not assume those models are available.`

const DefaultSearchKbNameDesc = `Optional knowledge base name. Leave empty to search automatically across the server's knowledge bases. Set it only to force a specific knowledge base.`

const DefaultReadDesc = `Read one chunk or section from a document, with provenance.

ARGUMENTS
- docSlug: result.document.id
- chunkID: result.location.chunk_id
- kbName: result.kb_name from that same result (use the exact value; do not guess)
- level: "section" to read the surrounding section when search reports multiple hits in one section

The response contains document metadata, location, citation_id, and an evidence block. source_confidence describes where the text came from. answer_relevance and completeness are reported as "unknown" — only you know the question, so judge those yourself and cite citation_id. A search score is a rank signal, not confidence.`

const DefaultReadKbNameDesc = `The kb_name from the search result you are reading. Use the exact value returned by knowledge_research.`

const DefaultListDesc = `List documents in a knowledge base, with optional filtering.`

const DefaultListKBsDesc = `List the available knowledge bases with their names and descriptions.`

const DefaultUploadDesc = `Upload files or directories to a knowledge base.

**kbName**: Required when no default KB is configured. Specify which knowledge base to upload to.`

const DefaultRemoveDesc = `Remove a document from a knowledge base by its slug.

**kbName**: Optional. If omitted, the document is removed from all knowledge bases.`
