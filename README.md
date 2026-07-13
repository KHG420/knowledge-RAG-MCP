# knowledge-mcp

MCP (Model Context Protocol) server that provides a local, file-based knowledge base with BM25 full-text search and optional hybrid (BM25 + embedding) retrieval.

## Features

- **Document ingestion** — PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX, MD, TXT
- **BM25 search** — Unicode-aware, CJK bigram-aware tokenizer
- **Hybrid search** — BM25 + dense embedding via Reciprocal Rank Fusion (optional, requires embedding API)
- **Paragraph-level chunking** with semantic merging and hierarchical (fine + coarse) sections
- **Paper metadata extraction** — title, authors, abstract for academic papers

## Installation

```bash
go install ./...
```

## Usage

### Configure

Set the data directory via environment variable (defaults to `<cwd>/.reasonix/knowledge/`):

```bash
export KNOWLEDGE_MCP_DATA_DIR=/path/to/data
```

### Run as MCP server (stdio transport)

```bash
knowledge-mcp
```

Add to your MCP client (e.g. Claude Desktop, Reasonix):

```json
{
  "mcpServers": {
    "knowledge": {
      "command": "/path/to/knowledge-mcp",
      "env": {
        "KNOWLEDGE_MCP_DATA_DIR": "/path/to/data"
      }
    }
  }
}
```

### MCP Tools

| Tool | Description |
|------|-------------|
| `knowledge_search` | BM25/hybrid search across all documents |
| `knowledge_read` | Read a specific chunk by docSlug + chunkID |
| `knowledge_list` | List all uploaded documents |
| `knowledge_upload` | Upload a file or batch-upload a directory |
| `knowledge_remove` | Remove a document and all its chunks |

### Supported file formats

`.md` `.txt` `.pdf` `.docx` `.odt` `.epub` `.html` `.xlsx` `.pptx`

## Storage layout

```
<data-dir>/
├── INDEX.md
├── .searchlog.jsonl
└── <document-slug>/
    ├── meta.json
    ├── CHUNKS.toml
    ├── source.<ext>
    └── chunks/
        ├── 000.md
        ├── 001.md
        └── ...
```

## Dependencies

- [tabula](https://github.com/tsawler/tabula) — document parsing (MIT)
- [toml](https://github.com/BurntSushi/toml) — index serialization (MIT)
- [mcp-go](https://github.com/mark3labs/mcp-go) — MCP protocol (MIT)

## License

MIT
