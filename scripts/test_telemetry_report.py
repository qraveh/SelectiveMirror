#!/usr/bin/env python3
"""
Unit tests for scripts/telemetry-report.py.

Runs as a plain Python script (no pytest required) for portability:
    python3 scripts/test_telemetry_report.py

Exits 0 if all assertions pass, non-zero otherwise.

Closes CLAIMS-MAP C-06 (AMBER → GREEN): k_anon_filter() correctly
suppresses cells below the floor, and md_cell_escape() neutralizes
markdown structural characters.
"""

from __future__ import annotations

import importlib.util
import os
import sys


def load_report_module():
    """Load scripts/telemetry-report.py as a module (the hyphen in the
    filename prevents `import` from working directly)."""
    here = os.path.dirname(os.path.abspath(__file__))
    path = os.path.join(here, "telemetry-report.py")
    spec = importlib.util.spec_from_file_location("telemetry_report", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("could not load telemetry-report.py spec")
    mod = importlib.util.module_from_spec(spec)
    # The script imports psycopg at module load. Stub it so the import
    # doesn't fail in environments without the driver.
    if "psycopg" not in sys.modules:
        import types
        stub = types.ModuleType("psycopg")
        stub.sql = types.ModuleType("psycopg.sql")
        sys.modules["psycopg"] = stub
        sys.modules["psycopg.sql"] = stub.sql
    spec.loader.exec_module(mod)
    return mod


# ---------------------------------------------------------------------------
# k_anon_filter (CLAIMS-MAP C-06)
# ---------------------------------------------------------------------------

def test_k_anon_filter_suppresses_below_floor(m):
    """Cells with count < K_ANONYMITY_FLOOR are filtered out."""
    rows = [
        {"client_version": "v0.9.0", "installs": 10},
        {"client_version": "v0.9.1", "installs": 4},
        {"client_version": "v0.9.2", "installs": 5},
        {"client_version": "v0.9.3", "installs": 1},
    ]
    visible = m.k_anon_filter(rows, "installs")
    assert m.K_ANONYMITY_FLOOR == 5, "regression: floor changed"
    assert len(visible) == 2, f"expected 2 visible, got {len(visible)}: {visible}"
    versions = {r["client_version"] for r in visible}
    assert versions == {"v0.9.0", "v0.9.2"}, f"unexpected visible set: {versions}"


def test_k_anon_filter_handles_missing_field(m):
    """Rows missing the count field are treated as 0 and filtered out."""
    rows = [
        {"client_version": "v1", "installs": 6},
        {"client_version": "v2"},  # missing field
        {"client_version": "v3", "installs": None},
    ]
    visible = m.k_anon_filter(rows, "installs")
    assert len(visible) == 1, f"expected 1 visible, got: {visible}"
    assert visible[0]["client_version"] == "v1"


def test_k_anon_filter_empty_input(m):
    """Empty input returns empty list."""
    assert m.k_anon_filter([], "installs") == []


def test_k_anon_filter_all_below_returns_empty(m):
    """If every row is below floor, result is empty (no rows leak)."""
    rows = [{"x": 1}, {"x": 2}, {"x": 3}, {"x": 4}]
    visible = m.k_anon_filter(rows, "x")
    assert visible == []


def test_k_anon_filter_handles_string_count_via_safe_int(m):
    """FINDING 20 (round-5 validation memo, 2026-05-03): defensive
    coding for schema drift. If the count column ever gets emitted as
    a string (TEXT cast / to_char view / downstream user query), the
    filter should not crash with TypeError. safe_int wraps the value
    in try/except, falling open to 0 → suppress.
    """
    # String values that ARE numeric: safe_int should parse them.
    rows = [{"x": "5"}, {"x": "4"}, {"x": "10"}]
    visible = m.k_anon_filter(rows, "x")
    assert len(visible) == 2  # "5" and "10" pass; "4" suppressed
    # String values that are NOT numeric: safe_int returns 0 → suppress.
    rows = [{"x": "abc"}, {"x": ""}, {"x": "5"}]
    visible = m.k_anon_filter(rows, "x")
    assert len(visible) == 1  # only "5" survives
    # Mixed types in one batch don't crash.
    rows = [{"x": 5}, {"x": "5"}, {"x": None}, {"x": "garbage"}]
    visible = m.k_anon_filter(rows, "x")
    assert len(visible) == 2  # int 5 and str "5"


def test_k_anon_guard_string_form(m):
    """k_anon_guard returns the count as string when ≥ floor; otherwise '<5'."""
    assert m.k_anon_guard(10) == "10"
    assert m.k_anon_guard(5) == "5"  # at the floor (inclusive)
    assert m.k_anon_guard(4) == "<5"
    assert m.k_anon_guard(0) == "<5"
    assert m.k_anon_guard(None) == "—"


# ---------------------------------------------------------------------------
# md_cell_escape (SM-166 verification)
# ---------------------------------------------------------------------------

def test_md_cell_escape_neutralizes_pipes(m):
    """The | character would break Markdown table cells; must be escaped."""
    out = m.md_cell_escape("a|b|c")
    assert "\\|" in out
    assert out == "a\\|b\\|c"


def test_md_cell_escape_strips_newlines(m):
    """Newlines break table cells; replaced with space."""
    out = m.md_cell_escape("line1\nline2\rline3\tline4")
    assert "\n" not in out
    assert "\r" not in out
    assert "\t" not in out


def test_md_cell_escape_drops_controls(m):
    """ASCII control chars (0x00-0x1F except already-handled, plus 0x7F)
    are dropped entirely."""
    s = "ab\x00c\x01d\x1fe\x7ff"
    out = m.md_cell_escape(s)
    assert out == "abcdef", f"unexpected output: {out!r}"


def test_md_cell_escape_truncates_long(m):
    """Values longer than max_len get an ellipsis."""
    long = "x" * 200
    out = m.md_cell_escape(long, max_len=20)
    assert len(out) == 20, f"expected len=20, got {len(out)}: {out}"
    assert out.endswith("…")


def test_md_cell_escape_handles_none(m):
    """None becomes the em-dash placeholder."""
    assert m.md_cell_escape(None) == "—"


def test_md_cell_escape_handles_numeric(m):
    """Numeric values pass through (no special chars)."""
    assert m.md_cell_escape(42) == "42"
    assert m.md_cell_escape(3.14) == "3.14"


def test_md_cell_escape_brackets_and_braces(m):
    """[ ] { } < > ` * _ \\ — all escaped."""
    out = m.md_cell_escape("[link](evil) {curly} <html> *bold* _ital_ `code`")
    for marker in ["\\[", "\\]", "\\{", "\\}", "\\<", "\\>", "\\*", "\\_", "\\`"]:
        assert marker in out, f"missing escape {marker!r} in output: {out!r}"


# ---------------------------------------------------------------------------
# KNOWN_BUG_KINDS (used in v2 'quiet kinds' computation)
# ---------------------------------------------------------------------------

def test_known_bug_kinds_matches_taxonomy(m):
    """KNOWN_BUG_KINDS must list the documented closed taxonomy. If
    PRIVACY.md / telemetry-architecture-v2.md add a new kind, this list
    has to update in lockstep."""
    expected = {"sync", "rclone", "watcher", "config", "service", "fs", "auth"}
    actual = set(m.KNOWN_BUG_KINDS)
    assert actual == expected, (
        f"KNOWN_BUG_KINDS drift: expected {expected}, got {actual}. "
        "Update PRIVACY.md / architecture-v2 first, then this test."
    )


# ---------------------------------------------------------------------------
# Driver
# ---------------------------------------------------------------------------

def main():
    m = load_report_module()
    tests = [
        test_k_anon_filter_suppresses_below_floor,
        test_k_anon_filter_handles_missing_field,
        test_k_anon_filter_empty_input,
        test_k_anon_filter_all_below_returns_empty,
        test_k_anon_filter_handles_string_count_via_safe_int,
        test_k_anon_guard_string_form,
        test_md_cell_escape_neutralizes_pipes,
        test_md_cell_escape_strips_newlines,
        test_md_cell_escape_drops_controls,
        test_md_cell_escape_truncates_long,
        test_md_cell_escape_handles_none,
        test_md_cell_escape_handles_numeric,
        test_md_cell_escape_brackets_and_braces,
        test_known_bug_kinds_matches_taxonomy,
    ]
    failures = []
    for t in tests:
        try:
            t(m)
            print(f"PASS {t.__name__}")
        except AssertionError as e:
            failures.append((t.__name__, str(e)))
            print(f"FAIL {t.__name__}: {e}")
        except Exception as e:
            failures.append((t.__name__, repr(e)))
            print(f"ERROR {t.__name__}: {e!r}")
    print()
    print(f"{len(tests) - len(failures)}/{len(tests)} passed")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
