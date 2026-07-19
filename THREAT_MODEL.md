# Grove Threat Model

Grove parses untrusted repositories, invokes optional local analyzers, and
persists symbols, source-derived text, paths, and edges in SQLite.

Assets are repository confidentiality, graph integrity, local credentials,
host availability, and release integrity. Boundaries include repository paths
and symlinks, parser/native-tool processes, the SQLite store, consumers of the
Go/CLI/MCP APIs, and the release pipeline.

Current controls exclude common credential files, constrain paths to the root,
avoid executing repository code during ordinary indexing, time-bound native
analysis, tag evidence source/confidence, and use manual-review outcomes when
certification cannot map a change. `grove doctor` exposes index and capability
state.

Residual risks include parser resource exhaustion, hostile compiler/project
configuration consumed by optional analyzers, dynamic/reflection gaps, and
local users reading the graph database. The store may contain private code
fragments and identifiers; protect and delete it with the repository. Do not
publish stores or attach them to issues.
