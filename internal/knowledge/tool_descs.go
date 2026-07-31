package knowledge

// Default MCP tool descriptions. These are the built-in, English+Chinese
// descriptions shown to LLM agents. Custom descriptions can be set via the
// Web UI (PUT /api/tool-descriptions) and take effect after a service restart.

const DefaultSearchDesc = `Semantic search across all documents in the knowledge base.

Use this tool by passing the user's original question. The system automatically handles:
- knowledge base routing
- query understanding
- keyword and semantic expansion
- retrieval strategy selection
- hybrid search
- reranking
- evidence confidence analysis

Do NOT manually select search mode, knowledge base, or rewrite the query.

Input:
- question: The user's original natural language question. Keep the original meaning and context.
- limit: Optional number of results (default 8, maximum 20).

The query should NOT be converted into keywords before calling this tool.
Do NOT add synonyms, translations, or technical terms manually.
The internal query analyzer handles Chinese/English expansion and domain terminology.

Examples:

User: "什么是同步横摇？"
→ knowledge_search(question="什么是同步横摇？")

User: "Ikeda方法包含哪些阻尼成分？"
→ knowledge_search(question="Ikeda方法包含哪些阻尼成分？")

User: "舭龙骨为什么降低横摇但增加阻力？"
→ knowledge_search(question="舭龙骨为什么降低横摇但增加阻力？")

The system automatically determines:
- which knowledge bases are relevant
- whether BM25, vector search, or hybrid retrieval is appropriate
- how many candidates are needed
- how to rank and filter evidence

Only provide the user's question. Let the retrieval system decide.

SEARCH BEHAVIOR:
The tool is designed for research and technical knowledge retrieval.
Prefer returning:
- primary source sections
- equations
- methodology descriptions
- experimental results
- definitions
Avoid:
- broad summaries without evidence
- unrelated documents
- speculative matches`

const DefaultSearchKbNameDesc = `Optional. Leave empty — the system auto-routes to the correct knowledge base(s). Only pass a kbName if you have a specific reason to force a particular KB.`

const DefaultReadDesc = `Read a specific chunk from a document in the knowledge base.

**kbName**: When you have search results, pass the same kbName from the search call to scope the read to the correct KB. If you don't know the KB, you may omit it — the system will search all KBs.

If search results show multiple hits from the same section (SectionHint field is non-empty), consider reading with level=section to get the full section context instead of just the individual chunk.`

const DefaultReadKbNameDesc = `Pass the same kbName from the search call that produced these results. If you don't know the KB, you may omit it — the system searches all KBs.`

const DefaultListDesc = `List documents in a knowledge base, with optional search/filter support.

**kbName**: Pass the same kbName used in the search call. If you don't know the KB, you may omit it.`

const DefaultListKBsDesc = `List all knowledge bases with their descriptions.

Returns the count of knowledge bases and each KB's name and description.
The description is the brief summary provided when the KB was created.
Knowledge bases without a description will show "(no description)".`

const DefaultUploadDesc = `Upload files or directories to a knowledge base.

**kbName**: Required when no default KB is configured. Specify which knowledge base to upload to.`

const DefaultRemoveDesc = `Remove a document from a knowledge base by its slug.

**kbName**: Optional. If omitted, the document is removed from all knowledge bases.`
