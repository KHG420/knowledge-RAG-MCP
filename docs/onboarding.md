# Onboarding verification

A minimal, honest path from a clean checkout to a working MCP endpoint.
Everything here uses the repository as-is; no forks, no extra dependencies.

## 0. Prerequisites

- Go 1.24+
- An existing MySQL/MariaDB instance you can create a database in

MySQL/MariaDB is required. There is no filesystem-only mode.

## 1. Build

```bash
go build -o knowledge-mcp .
```

Dependencies are vendored, so this works without network access.

## 2. Verify the setup wizard is reachable without side effects

The wizard only reads stdin and writes `knowledge-mcp.toml` after an explicit
confirmation. Quitting at any step must not create a config file or touch MySQL:

```bash
printf '2\nq\n' | ./knowledge-mcp setup
test ! -f knowledge-mcp.toml && echo "no config written (expected)"
```

## 3. Configure

Create the database first, then run the wizard:

```bash
./knowledge-mcp setup
```

Or write `knowledge-mcp.toml` next to the binary:

```toml
mysql_dsn = "user:password@tcp(127.0.0.1:3306)/knowledge_rag?parseTime=true"
# Optional: protect the management API and the MCP HTTP endpoints.
# api_token = "change-me"
```

Precedence: `knowledge-mcp.toml` > environment variables (only when the TOML
file is absent) > built-in defaults.

## 4. Start

```bash
./knowledge-mcp serve          # management UI :8085 + MCP HTTP :8086
./knowledge-mcp serve --mcp    # MCP HTTP only
./knowledge-mcp manage         # management UI only
./knowledge-mcp stdio          # stdio MCP (no token required)
```

## 5. Create a KB and upload

Open `http://localhost:8085`, create a knowledge base, and upload a document.
Wait for the upload task to reach `done`.

## 6. Verify the MCP endpoint

Without `api_token`:

```bash
curl -sS http://localhost:8086/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"onboarding","version":"1"}}}'
```

With `api_token = "change-me"` the same request must carry the bearer token:

```bash
curl -sS http://localhost:8086/mcp \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer change-me' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"onboarding","version":"1"}}}'
```

A request without the token returns HTTP 401. The token applies to `/mcp`,
`/sse`, `/message`, and the management API.

## 7. Confirm the registered tools

Call `tools/list` (same endpoint). You should see exactly three tools:
`knowledge_research`, `knowledge_read`, `knowledge_list_kbs`.

`knowledge_research` returns a JSON envelope with `results` (always an array),
`searched_kbs`, `failed_kbs`, `warnings`, and `coverage` (`complete`/`partial`).
`coverage` describes the search, not whether the results answer the question:
`complete` means every knowledge base the server selected and attempted was
searched successfully, which does not imply that every available KB was searched
or that an empty `results` array means "nothing exists anywhere".
`knowledge_read` returns provenance plus an evidence block whose
`answer_relevance` and `completeness` are `unknown` by design.
