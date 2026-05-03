"""Shared helper for actionable third-party-dependency errors.

Several telemetry scripts (`scripts/telemetry-report.py`,
`scripts/telemetry-debug.py`, `scripts/telemetry-v2-smoke-test.py`,
plus the validation scripts in `system-validation/`) need either
`psycopg` (v3, preferred) / `psycopg2-binary` (fallback) for live
Supabase access, or `requests` for live Worker access.

Pre-fix problem (FINDING 37, round 11): every script imports its
deps at module-load time. If the dep is missing — common in WSL or
on a fresh Python install where the operator hasn't yet
`pip install`ed — the script crashes BEFORE argparse runs. Even
`--help` fails. The error message is generic ("install with: pip
install psycopg2-binary") and doesn't tell the operator WHICH
Python interpreter is missing the dep, which is a sharp problem on
machines with multiple Python installs (Windows + WSL,
system Python + venv, etc.).

This helper provides two functions:

  - `require(module_name, install_hint, alt_modules=())` — try to
    import; on ImportError, print an actionable error block (with
    interpreter path, version, and the exact command to run) and
    exit cleanly with code 2.

  - `require_psycopg()` and `require_requests()` — convenience
    wrappers for the two specific modules these scripts need.

Pattern in each consumer:

  1. argparse runs first (so --help works even with missing deps).
  2. After args.parse_args(), call `require_psycopg()` (or
     `require_requests()`) BEFORE any code that uses the module.
  3. The function returns the imported module so the caller can
     bind it: `psycopg = require_psycopg()`.

This file has NO third-party imports of its own. It must be
importable from any Python install, however bare.
"""
from __future__ import annotations

import sys


def _interpreter_info() -> str:
    """Return a multi-line description of the Python interpreter
    currently running, useful for the `pip install` command line."""
    return (f"  Python interpreter: {sys.executable}\n"
            f"  Python version:     {sys.version.split()[0]}\n"
            f"  Platform:           {sys.platform}\n")


def _detect_pep668_managed() -> bool:
    """Return True if this Python install is in a PEP-668 'externally
    managed' environment that blocks system-wide `pip install`.

    On Debian/Ubuntu 22.04+ (and Fedora 38+), system `pip install` is
    blocked by default to prevent breaking the OS Python. The marker
    is a `EXTERNALLY-MANAGED` file next to the stdlib. Detect it so
    the error message can suggest the right fix."""
    import sysconfig
    stdlib = sysconfig.get_path("stdlib")
    if not stdlib:
        return False
    import os
    return os.path.exists(os.path.join(stdlib, "EXTERNALLY-MANAGED"))


def _detect_distro() -> str:
    """Best-effort distro detection. Returns 'debian' / 'redhat' /
    'arch' / 'unknown'."""
    import os
    if os.path.exists("/etc/debian_version"):
        return "debian"
    if os.path.exists("/etc/redhat-release") or os.path.exists("/etc/fedora-release"):
        return "redhat"
    if os.path.exists("/etc/arch-release"):
        return "arch"
    return "unknown"


def require(module_name: str, install_pip_args: str,
            alt_modules: tuple = ()):
    """Import `module_name` (or any of `alt_modules`); on failure,
    print an actionable error and `sys.exit(2)`.

    `install_pip_args` is the arg string passed to `pip install` to
    fetch the package, e.g. `'psycopg[binary]'` or `'requests'`.

    Returns the imported module (the first one that succeeded).
    """
    candidates = (module_name,) + tuple(alt_modules)
    for candidate in candidates:
        try:
            return __import__(candidate)
        except ImportError:
            continue

    # All candidates failed. Print actionable error.
    sys.stderr.write(
        f"ERROR: this script needs `{module_name}`")
    if alt_modules:
        sys.stderr.write(f" (or one of: {', '.join(alt_modules)})")
    sys.stderr.write(", but none of them is importable.\n\n")
    sys.stderr.write("Interpreter information:\n")
    sys.stderr.write(_interpreter_info())

    is_managed = _detect_pep668_managed()
    distro = _detect_distro()

    sys.stderr.write("\nInstall via ONE of these (try them in order):\n\n")

    # Path 1: venv (works everywhere, recommended for managed envs)
    sys.stderr.write("  [recommended] Create a venv just for SelectiveMirror's tools:\n")
    sys.stderr.write(f"    {sys.executable} -m venv ~/.smirror-venv\n")
    if sys.platform == "win32":
        sys.stderr.write("    ~/.smirror-venv/Scripts/Activate.ps1   (PowerShell)\n")
        sys.stderr.write("    source ~/.smirror-venv/Scripts/activate (Git Bash)\n")
    else:
        sys.stderr.write("    source ~/.smirror-venv/bin/activate\n")
    sys.stderr.write(f"    pip install '{install_pip_args}'\n")
    sys.stderr.write("    # then re-run the script from the activated venv shell.\n\n")

    # Path 2: --user install (works for non-managed envs)
    if not is_managed:
        sys.stderr.write("  [simpler] User-local install (no venv needed):\n")
        sys.stderr.write(f"    {sys.executable} -m pip install --user '{install_pip_args}'\n\n")

    # Path 3: distro package manager. For Debian/Ubuntu, suggest
    # python3-psycopg2 — it's psycopg v2 (older), but it's available
    # on every Ubuntu since 18.04 whereas python3-psycopg (v3) only
    # ships on Ubuntu 24.04+. The script's require_psycopg() falls
    # back to v2 if v3 isn't there, so v2-via-apt is fully sufficient.
    if distro == "debian":
        apt_pkg = {
            "psycopg[binary]": "python3-psycopg2",
            "requests": "python3-requests",
        }.get(install_pip_args, "python3-" + module_name)
        sys.stderr.write("  [system-wide] Install the Debian/Ubuntu package:\n")
        sys.stderr.write(f"    sudo apt update && sudo apt install -y {apt_pkg}\n")
        if install_pip_args == "psycopg[binary]":
            sys.stderr.write("    # (this is psycopg v2; the script auto-falls-back to v2 when v3 is missing)\n")
        sys.stderr.write("\n")
    elif distro == "redhat":
        sys.stderr.write("  [system-wide] Install via dnf (Fedora/RHEL):\n")
        sys.stderr.write(f"    sudo dnf install -y python3-{module_name}\n\n")

    # Path 4: --break-system-packages (last resort for managed envs)
    if is_managed:
        sys.stderr.write(
            "  [override, NOT recommended] Bypass the PEP-668 'externally\n"
            "  managed' guard (this Python install was flagged by your\n"
            "  distro as managed; pip-installing into it can break OS tools):\n")
        sys.stderr.write(f"    {sys.executable} -m pip install --break-system-packages '{install_pip_args}'\n\n")

    sys.stderr.write(
        "Notes:\n"
        "  - On Windows + WSL, the WSL Python install is separate from\n"
        "    the Windows one. Each shell needs its own install.\n"
        "  - If `pip` is missing, run\n"
        f"    `{sys.executable} -m ensurepip` first (or your distro's\n"
        "    `python3-pip` package).\n")
    sys.exit(2)


def require_psycopg():
    """Convenience wrapper. Prefers psycopg v3; falls back to psycopg2."""
    return require("psycopg", "psycopg[binary]", alt_modules=("psycopg2",))


def require_requests():
    """Convenience wrapper for the `requests` HTTP library."""
    return require("requests", "requests")
