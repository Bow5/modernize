#!/usr/bin/env python3
"""Remove if _root/_recv != nil guards inserted by nilableMethodGuards."""

from __future__ import annotations

import re
import sys
from pathlib import Path


def find_block_end(lines: list[str], start: int) -> int:
    depth = 0
    for i in range(start, len(lines)):
        depth += lines[i].count("{")
        depth -= lines[i].count("}")
        if depth == 0 and "}" in lines[i]:
            return i
    raise ValueError(f"unbalanced braces at line {start + 1}")


def parse_if_header(header_lines: list[str]) -> tuple[str, str, str] | None:
    """Return (indent, init, temp) for a nil-guard if header."""
    text = "\n".join(header_lines)
    m = re.search(
        r"^(\s*)if _(root|recv) :=\s*(.+?);\s*_\2\s*!=\s*nil\s*\{\s*$",
        text,
        re.DOTALL,
    )
    if not m:
        return None
    indent, kind, init = m.group(1), m.group(2), m.group(3).strip()
    return indent, init, f"_{kind}"


def unwrap_file(path: Path) -> bool:
    lines = path.read_text().splitlines()
    out: list[str] = []
    i = 0
    changed = False

    while i < len(lines):
        line = lines[i]
        if not re.match(r"^\s*if _(root|recv) :=", line):
            out.append(line)
            i += 1
            continue

        header_start = i
        header_end = i
        while header_end < len(lines) and "{" not in lines[header_end]:
            header_end += 1
        if header_end >= len(lines):
            out.append(line)
            i += 1
            continue

        header = lines[header_start : header_end + 1]
        parsed = parse_if_header(header)
        if parsed is None:
            out.append(line)
            i += 1
            continue

        indent, init, temp = parsed
        body_start = header_end + 1
        body_end = find_block_end(lines, header_end)
        body = lines[body_start:body_end]

        inner_indent = indent + "\t"
        for bline in body:
            if bline.startswith(inner_indent):
                bline = bline[len(inner_indent) :]
            elif bline.strip() == "":
                pass
            else:
                # keep unexpected indentation as-is after stripping one tab
                bline = bline[1:] if bline.startswith("\t") else bline
            bline = bline.replace(temp + ".", init + ".")
            bline = bline.replace(temp, init)
            # collapse split selector: init\n\t\t.Method -> init.Method
            if out and re.match(r"^\s*\.\w", bline) and out[-1].rstrip().endswith(init):
                out[-1] = out[-1].rstrip() + bline.lstrip()
                continue
            out.append(bline)

        i = body_end + 1
        changed = True

    new_text = "\n".join(out)
    if not new_text.endswith("\n"):
        new_text += "\n"
    old = path.read_text()
    if old != new_text:
        path.write_text(new_text)
        return True
    return False


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: unwrap_nil_guards.py <file>...", file=sys.stderr)
        return 2
    any_changed = False
    for arg in sys.argv[1:]:
        path = Path(arg)
        if unwrap_file(path):
            print(f"unwrapped {path}")
            any_changed = True
    return 0 if any_changed or len(sys.argv) > 1 else 1


if __name__ == "__main__":
    raise SystemExit(main())
