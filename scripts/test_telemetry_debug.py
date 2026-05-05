#!/usr/bin/env python3
"""
Focused tests for telemetry-operator-report.py — round-8 hardening.

The operator-report script is a debug tool, not a release artifact,
but it has enough security-relevant logic (Markdown injection
defense, hostname leakage, publish-safety guard) that the
sanitizer-equivalent functions deserve direct test coverage.

These tests do NOT spin up a live database; they exercise the pure
helper functions (md_cell_escape, project_id_from_url, banner_block,
classify_version) against representative inputs from the round-8
findings.

Run:
    python3 system-validation/test_telemetry_operator_report.py
"""
from __future__ import annotations

import importlib.util
import pathlib
import sys


def load_module():
    """Load telemetry-debug.py as a module despite the hyphen in
    the filename. (Renamed from telemetry-operator-report.py in
    0.9.10x-dev when the script moved scripts/ alongside the
    canonical published digest — panel scope decision Option C.)"""
    here = pathlib.Path(__file__).resolve().parent
    src = here / "telemetry-debug.py"
    spec = importlib.util.spec_from_file_location("telemetry_debug", src)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"could not load {src}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


# ---------------------------------------------------------------------------
# md_cell_escape — FINDING 26-29 hardening
# ---------------------------------------------------------------------------

def test_md_cell_escape_none(m):
    assert m.md_cell_escape(None) == "—"


def test_md_cell_escape_empty(m):
    assert m.md_cell_escape("") == ""


def test_md_cell_escape_simple_string(m):
    assert m.md_cell_escape("hello") == "hello"


def test_md_cell_escape_strips_newlines_FINDING_26(m):
    """FINDING 26: embedded newline must not split a Markdown row.
    md_cell_escape converts to single space."""
    out = m.md_cell_escape("1.0.0\nINJECTED")
    assert "\n" not in out, f"newline survived: {out!r}"
    assert "INJECTED" in out, f"content lost: {out!r}"
    out2 = m.md_cell_escape("1.0.0\r\n## fake heading")
    assert "\n" not in out2 and "\r" not in out2


def test_md_cell_escape_escapes_lt_gt_FINDING_27(m):
    """FINDING 27: `<` and `>` escaped (HTML-injection defense).
    The defense is backslash-escape (CommonMark + GFM treat `\\<` as
    literal text rather than HTML tag). The angle brackets remain in
    the output but each MUST be backslash-preceded."""
    out = m.md_cell_escape("1.0.0<script>alert(1)</script>")
    assert "\\<script" in out, f"<script not escaped: {out!r}"
    assert "\\</script" in out, f"</script not escaped: {out!r}"
    # Structural: every < and > is preceded by \
    for i, ch in enumerate(out):
        if ch in ("<", ">"):
            assert i > 0 and out[i - 1] == "\\", \
                f"unescaped {ch!r} at index {i} in {out!r}"


def test_md_cell_escape_strips_control_chars_FINDING_28(m):
    """FINDING 28: control characters (incl. RTL override U+202E)
    stripped."""
    rtl = "1.0.0‮injected‬"
    out = m.md_cell_escape(rtl)
    assert "‮" not in out, f"RTL override survived: {out!r}"
    assert "‬" not in out, f"PDF (pop directional formatting) survived: {out!r}"
    # Backspace / bell etc.
    out2 = m.md_cell_escape("a\x08b\x07c")
    assert out2 == "abc", f"control chars survived: {out2!r}"


def test_md_cell_escape_truncates_long_FINDING_29(m):
    """FINDING 29: long strings truncated at 120 chars with `…`."""
    s = "X" * 200
    out = m.md_cell_escape(s)
    assert len(out) <= 120, f"length {len(out)} > 120"
    assert out.endswith("…"), f"truncation marker missing: {out!r}"


def test_md_cell_escape_pipe_escaped(m):
    """Markdown table column separator must be escaped."""
    out = m.md_cell_escape("a|b|c")
    assert out == "a\\|b\\|c"


def test_md_cell_escape_collapses_whitespace(m):
    """Multiple spaces collapse to one (table layout sanity)."""
    out = m.md_cell_escape("a    b\t\tc")
    assert out == "a b c"


def test_md_cell_escape_brackets(m):
    """Square + curly brackets escaped (Markdown link / extension syntax)."""
    out = m.md_cell_escape("a[link]{attr}")
    assert "\\[" in out and "\\]" in out
    assert "\\{" in out and "\\}" in out


# ---------------------------------------------------------------------------
# project_id_from_url — FINDING 31
# ---------------------------------------------------------------------------

def test_project_id_pooler_url_FINDING_31(m):
    """FINDING 31: extract project ID without leaking AWS hostname."""
    url = "postgresql://postgres.exampleprojectref:secret@aws-0-eu-west-1.pooler.supabase.com:6543/postgres"
    pid = m.project_id_from_url(url)
    assert pid == "exampleprojectref"
    # The hostname / region should NEVER appear in the extraction.
    assert "aws" not in pid
    assert "eu-west" not in pid
    assert "pooler" not in pid


def test_project_id_unknown_form(m):
    """Unknown URL shape falls through to 'unknown' (no crash, no leak)."""
    assert m.project_id_from_url("not-a-url") == "unknown"
    assert m.project_id_from_url("") == "unknown"
    assert m.project_id_from_url(None) == "unknown"


# ---------------------------------------------------------------------------
# banner_block — FINDING 30
# ---------------------------------------------------------------------------

def test_banner_block_says_not_for_publication_FINDING_30(m):
    """FINDING 30: every operator-debug output must be marked
    INTERNAL / DO NOT PUBLISH at the top so a casual copy-paste-
    publish doesn't leak un-floor-filtered data.

    Panel item 5: banner phrasing is "DO NOT
    PUBLISH" rather than "NOT FOR PUBLICATION" — shorter, more
    imperative. Both signal the same thing; the test accepts
    either."""
    block = m.banner_block()
    text = "\n".join(block)
    # Required phrases that signal "internal only." Either banner
    # phrasing satisfies the contract.
    assert "DO NOT PUBLISH" in text or "NOT FOR PUBLICATION" in text, \
        f"banner missing publish-warning phrase: {text!r}"
    assert "INTERNAL" in text
    assert "k-anonymity" in text
    # The banner must point at the publish-safe alternative.
    assert "telemetry-report.py" in text


# ---------------------------------------------------------------------------
# classify_version (regression — round-7b had this; round-8 added test markers)
# ---------------------------------------------------------------------------

def test_classify_version_released(m):
    releases = {"0.9.26": __import__("datetime").date(2026, 4, 18)}
    rdate, cls = m.classify_version("0.9.26", releases)
    assert rdate == "2026-04-18"
    assert cls == "released"


def test_classify_version_dev(m):
    rdate, cls = m.classify_version("0.9.95-dev", {})
    assert "dev" in cls


def test_classify_version_test_marker_round8(m):
    """Round-8 added -r8- pattern to the test-marker list."""
    rdate, cls = m.classify_version("1.0.0-r8-adversarial", {})
    assert cls == "test-marker"
    rdate, cls = m.classify_version("0.9.95-r7b", {})
    assert cls == "test-marker"


def test_classify_version_unknown(m):
    rdate, cls = m.classify_version("99.99.99", {})
    assert "unknown" in cls


# ---------------------------------------------------------------------------
# Adversarial integration: verify the actual injection vectors from
# the round-8 memo are neutralized.
# ---------------------------------------------------------------------------

def test_round8_finding_26_full_injection(m):
    """Round-8 FINDING 26 reproducer: a version string designed to
    inject a fake heading + table breakage."""
    payload = "1.0.0\n## Compromised\nI control this section\n"
    out = m.md_cell_escape(payload)
    # The newlines must be gone — no row split.
    assert "\n" not in out
    # The `#` is fine alone (heading only at line-start; we collapsed
    # newlines), but we should also sanity-check that the raw `##`
    # pattern doesn't appear in a way Markdown could parse as a heading.
    # Since we stripped newlines, `## Compromised` is now inline; not
    # a heading. ✓


def test_round8_finding_27_html_full_injection(m):
    """Round-8 FINDING 27 reproducer: img-onerror payload that
    survives some Markdown sanitizers when `<img` is unescaped."""
    payload = "1.0.0<img src=x onerror=alert(1)>"
    out = m.md_cell_escape(payload)
    assert "\\<img" in out, f"<img not escaped: {out!r}"
    # Structural: every angle bracket is escaped.
    for i, ch in enumerate(out):
        if ch in ("<", ">"):
            assert i > 0 and out[i - 1] == "\\", \
                f"unescaped {ch!r} at index {i} in {out!r}"


def test_round8_finding_28_rtl_full_injection(m):
    payload = "1.0.0‮injected‬-r8-adv"
    out = m.md_cell_escape(payload)
    assert "‮" not in out and "‬" not in out


# ---------------------------------------------------------------------------
# Test runner (no pytest dependency)
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    m = load_module()
    tests = [
        test_md_cell_escape_none,
        test_md_cell_escape_empty,
        test_md_cell_escape_simple_string,
        test_md_cell_escape_strips_newlines_FINDING_26,
        test_md_cell_escape_escapes_lt_gt_FINDING_27,
        test_md_cell_escape_strips_control_chars_FINDING_28,
        test_md_cell_escape_truncates_long_FINDING_29,
        test_md_cell_escape_pipe_escaped,
        test_md_cell_escape_collapses_whitespace,
        test_md_cell_escape_brackets,
        test_project_id_pooler_url_FINDING_31,
        test_project_id_unknown_form,
        test_banner_block_says_not_for_publication_FINDING_30,
        test_classify_version_released,
        test_classify_version_dev,
        test_classify_version_test_marker_round8,
        test_classify_version_unknown,
        test_round8_finding_26_full_injection,
        test_round8_finding_27_html_full_injection,
        test_round8_finding_28_rtl_full_injection,
    ]
    failures = 0
    for t in tests:
        try:
            t(m)
            print(f"PASS {t.__name__}")
        except AssertionError as e:
            failures += 1
            print(f"FAIL {t.__name__}: {e}")
        except Exception as e:
            failures += 1
            print(f"ERROR {t.__name__}: {type(e).__name__}: {e}")
    print()
    if failures:
        print(f"{failures}/{len(tests)} failed")
        sys.exit(1)
    print(f"{len(tests)}/{len(tests)} passed")
