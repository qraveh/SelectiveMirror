"""Single source of truth for Markdown escaping in SelectiveMirror's
telemetry reports.

Both consumers import from here:

  - `scripts/telemetry-report.py` — canonical weekly digest, k-anon
    floor of 5, published via .github/workflows/telemetry-digest.yml.
  - `system-validation/telemetry-operator-report.py` — operator debug
    view, no k-anon floor, NOT for publication.

Background: PANEL-2 (BMAD multi-role panel review, 2026-05-03) found
two near-identical `md_*` escape functions in the two scripts with
subtle differences. Divergence between the operator-debug and
publish-safe escapers is *itself* a privacy bug — the operator might
fix a sanitization gap in their version and forget the published one,
or vice versa. This module is the fix: one function, one source of
truth, both consumers import.

The hardening here is the UNION of the two prior implementations
plus the bidi/zero-width Unicode strip that BOTH prior versions
missed (PANEL-2 / round-8 FINDING 28).

DO NOT add a copy of these functions back into either consumer.
If a render path needs different behavior, parameterize it here
(see `max_len`).
"""
from __future__ import annotations

# Markdown structural characters that need escaping inside a table cell.
# Order matters: backslash MUST be first so escapes added below aren't
# double-escaped. <,>,{,} are escaped because some Markdown renderers
# (Confluence, Notion, custom dashboards) pass HTML through.
_MD_ESCAPE_PAIRS = (
    ("\\", "\\\\"),
    ("|", "\\|"),
    ("`", "\\`"),
    ("*", "\\*"),
    ("_", "\\_"),
    ("[", "\\["),
    ("]", "\\]"),
    ("<", "\\<"),
    (">", "\\>"),
    ("{", "\\{"),
    ("}", "\\}"),
)

# Unicode codepoints that are visually deceptive in Markdown table cells.
# These survive ASCII-control stripping (cp < 0x20) but break visual
# parsing of tables in renderers that honor them. Drop them entirely.
#
# Ranges chosen per Unicode TR9 (bidirectional algorithm):
#   U+200B..U+200D — zero-width space / non-joiner / joiner
#   U+200E..U+200F — directional marks (LRM / RLM)
#   U+202A..U+202E — bidi embedding/override (LRE/RLE/PDF/LRO/RLO)
#   U+2066..U+2069 — directional isolates (LRI/RLI/FSI/PDI)
#   U+FFF9..U+FFFB — interlinear annotation
_DECEPTIVE_UNICODE = frozenset(
    list(range(0x200B, 0x2010))      # zero-width + directional marks
    + list(range(0x202A, 0x202F))    # bidi embedding / override
    + list(range(0x2066, 0x206A))    # directional isolates
    + list(range(0xFFF9, 0xFFFC))    # interlinear annotation
)


def md_cell_escape(s, max_len: int = 120) -> str:
    """Sanitize a value for safe inclusion in a Markdown table cell.

    Hardening applied (in order):
      1. None → em-dash placeholder.
      2. CR / LF / TAB collapsed to a single space (a table cell
         must be exactly one logical line).
      3. ASCII control characters (cp < 0x20) and DEL (0x7F) dropped.
      4. Bidi/directional/zero-width Unicode dropped (PANEL-2).
      5. Markdown structural characters escaped:
         backslash, pipe, backtick, asterisk, underscore,
         brackets, angle brackets, braces.
      6. Whitespace runs collapsed to a single space.
      7. Truncated to `max_len` chars with U+2026 ellipsis (default
         120 — wide enough for an error signature, narrow enough to
         keep tables readable on phones).

    Numerics (int / float / Decimal) are coerced via str() first;
    they contain only safe characters and pass through unchanged.

    Why this matters: server-side rollup tables accept any TEXT for
    `client_version` and `bug_kind` (the schema has no length or
    character restrictions). A buildKey-bearing attacker could submit
    a contribution with newlines or HTML tags in those fields. This
    function neutralizes the bytes before rendering. Without it, a
    single malicious contribution could inject an entire fake section
    into the operator report or break Markdown table parsing.
    """
    if s is None:
        return "—"
    s = str(s)
    cleaned = []
    for ch in s:
        cp = ord(ch)
        if ch in ("\r", "\n", "\t"):
            cleaned.append(" ")
        elif cp < 0x20 or cp == 0x7F:
            continue
        elif cp in _DECEPTIVE_UNICODE:
            continue
        else:
            cleaned.append(ch)
    s = "".join(cleaned)
    for ch, esc in _MD_ESCAPE_PAIRS:
        s = s.replace(ch, esc)
    s = " ".join(s.split())
    if len(s) > max_len:
        s = s[: max_len - 1] + "…"
    return s
