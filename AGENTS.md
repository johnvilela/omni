<!-- memoria:start -->
## Project memory (memoria)

Curated long-term memory from past agent sessions lives in `wiki/`:
decisions made, rules to follow, gotchas hit, concepts explained.
Before non-trivial changes: read `wiki/index.md`, then grep `wiki/`
for keywords. Pages carry YAML `tags:` frontmatter for topic lookup.
Prefer the memoria MCP tools when available: memoria_search,
memoria_recall, memoria_digest, memoria_consolidate, memoria_lint,
memoria_write_page, memoria_delete_page.
To recall what a past session did, call memoria_recall (read-only).
memoria_digest WRITES the session's wiki page — only when the user
asks to save the session.
<!-- memoria:end -->
