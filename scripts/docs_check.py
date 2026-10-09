#!/usr/bin/env python3
"""Fail when ARCHITECTURE.md's diagrams drift from their sources.

Every docs/architecture/mermaid/*.mmd file must appear verbatim as a
```mermaid block of ARCHITECTURE.md, and every such block must be one of
them. Edit the .mmd file and the block together.

It also rejects ";" in a sequence diagram: Mermaid reads it as a statement
separator, so text after it in a message or note fails to parse (render
check: docker run minlag/mermaid-cli -i <file>.mmd).

Usage: docs_check.py  (from the repository root)
"""
import pathlib
import re
import sys

doc = pathlib.Path("ARCHITECTURE.md").read_text()
blocks = [b.strip("\n") for b in re.findall(r"```mermaid\n(.*?)```", doc, re.S)]
sources = {p.name: p.read_text().strip("\n") for p in sorted(pathlib.Path("docs/architecture/mermaid").glob("*.mmd"))}

problems = [f"{name}: not embedded verbatim in ARCHITECTURE.md" for name, src in sources.items() if src not in blocks]
problems += [f"ARCHITECTURE.md mermaid block {i + 1} matches no .mmd file" for i, b in enumerate(blocks) if b not in sources.values()]
for name, src in sources.items():
    if src.lstrip().startswith("sequenceDiagram"):
        for n, line in enumerate(src.splitlines(), 1):
            if ";" in line:
                problems.append(f"{name}:{n}: ';' ends a sequence-diagram statement — use ',' or '—'")
for p in problems:
    print(f"docs-check: {p}", file=sys.stderr)
if problems:
    sys.exit(1)
print(f"docs-check: {len(sources)} diagrams in sync")
