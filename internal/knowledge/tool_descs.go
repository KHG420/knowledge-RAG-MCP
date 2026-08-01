package knowledge

// Default MCP tool descriptions. These are the built-in, English+Chinese
// descriptions shown to LLM agents. Custom descriptions can be set via the
// Web UI (PUT /api/tool-descriptions) and take effect after a service restart.

const DefaultSearchDesc = `Semantic research search across all documents in the knowledge base.

This tool is designed for AI agents performing autonomous research.
The agent may decompose complex research questions into multiple targeted searches when needed.

Use this tool to retrieve technical evidence, primary source materials, methodologies, equations, experimental results, and definitions from the knowledge base.

SEARCH STRATEGY:

For simple factual questions:
- Pass the user's original question directly.
- Avoid unnecessary keyword extraction or query expansion.

For complex research tasks:
- You may decompose the problem into multiple focused research questions.
- You may generate specialized queries targeting different aspects of the topic.
- Multiple tool calls are allowed when they improve research coverage.

Examples:

User: "什么是同步横摇？"

Preferred:
{
  "question": "什么是同步横摇？",
  "limit": 8
}

User: "Ikeda方法包含哪些阻尼成分？"

Preferred:
{
  "question": "Ikeda方法包含哪些阻尼成分？",
  "limit": 10
}

User: "分析船舶初步设计阶段耐波性评估技术路线"

Reasonable research decomposition:

Query 1: "船舶初步设计阶段 耐波性 航行性能评估方法"
Query 2: "RAO 垂向运动 升沉 纵摇 船舶耐波性计算"
Query 3: "Lewis保角映射 Tasai方法 Salvesen切片理论 水动力系数"
Query 4: "Cummins方程 时域运动模拟 流体记忆效应"

The system automatically handles:
- knowledge base routing
- semantic understanding
- terminology expansion
- BM25 and vector hybrid retrieval
- candidate generation
- reranking
- evidence confidence analysis

The agent should focus on research planning and answer synthesis.
The retrieval system focuses on finding reliable evidence.

INPUT:

question:
The research question or search query.

For best results:
- Use natural language questions or focused research queries.
- Include sufficient context for domain-specific searches.
- Do not manually force retrieval mode selection.

limit:
Optional number of results. Default: 8. Maximum: 20.

OUTPUT:

Returns ranked evidence from relevant documents, including:
- document metadata
- matched passages
- relevance score
- confidence information
- source context

Prioritize:
- original research papers
- technical reports
- methodology descriptions
- equations and models
- experimental validation results

Avoid relying only on broad summaries when primary evidence is available.`

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
