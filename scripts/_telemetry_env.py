"""Shared helper for auto-loading the SelectiveMirror deploy env file.

Both `scripts/telemetry-report.py` (canonical weekly digest) and
`scripts/telemetry-debug.py` (operator debug view) need `DATABASE_URL`;
`telemetry-debug.py` also wants `TELEMETRY_MASTER_KEY` for the optional
Worker integration proof. These live in `~/.smirror-deploy.env`, a
bash-sourceable file.

The problem this helper solves:

  - PowerShell has no `source` / `.` for bash files.
  - WSL2 maps `~` to the Linux home, not the Windows home, so a file
    living at `C:\\Users\\<you>\\.smirror-deploy.env` is invisible to
    `source ~/.smirror-deploy.env` from inside the WSL shell.
  - A maintainer who's already-sourced the file in their current
    shell (bash on Windows / Git Bash / native Linux) doesn't need
    this helper to do anything.

Strategy: when the script starts, call `load_deploy_env()` which:
  1. Checks if `DATABASE_URL` is already in os.environ. If yes, nothing
     to do — the operator (or their `source`) already wired it up.
  2. Otherwise, walks a list of well-known locations looking for
     `.smirror-deploy.env`. The first one found is parsed and its
     variables are added to os.environ for the lifetime of the
     Python process.
  3. Variables already in os.environ are NEVER overwritten. The
     operator's manual `set DATABASE_URL=...` always wins over the
     deploy-env file.

Lookup order:
  1. `$SMIRROR_DEPLOY_ENV` env var pointing at a path (override).
  2. `~/.smirror-deploy.env`              (Linux home / Windows home in Git Bash).
  3. `$USERPROFILE/.smirror-deploy.env`   (Windows env var; PowerShell).
  4. `/mnt/c/Users/$USER/.smirror-deploy.env`  (WSL2 → Windows home bridge).
  5. `/mnt/c/Users/<all common usernames>` if WSL2 user differs from Windows user.
  6. Repo-root `.smirror-deploy.env`       (project-local checkout).

Format expected: bash-sourceable lines like
  `export DATABASE_URL=postgresql://...`
  `export TELEMETRY_MASTER_KEY=abc123...`
Comments (#), blank lines, and inline `export` are tolerated.
Single- and double-quoted values are stripped.

Returns: a tuple `(loaded_path, vars_loaded)` for diagnostic logging.
`loaded_path` is `None` if no env file was found; `vars_loaded` is
the count of variables actually written to os.environ (excluding
those that were already set).
"""
from __future__ import annotations

import os
import sys


def _candidate_paths():
    """Yield candidate paths in priority order."""
    # 1. Explicit override
    override = os.environ.get("SMIRROR_DEPLOY_ENV")
    if override:
        yield override

    # 2. Standard ~ expansion (works in bash on Windows + native Linux + Git Bash).
    yield os.path.expanduser("~/.smirror-deploy.env")

    # 3. Windows USERPROFILE (works in PowerShell + cmd.exe + WSL).
    user_profile = os.environ.get("USERPROFILE")
    if user_profile:
        yield os.path.join(user_profile, ".smirror-deploy.env")

    # 4. WSL2 bridge to Windows home. WSL's $USER is the LINUX user
    # name; the Windows username is often but not always the same.
    # Try the common candidates: $USER, $WSL_USER, $WINDOWS_USER.
    if os.path.exists("/mnt/c/Users"):
        candidates = []
        for var in ("USER", "WSL_USER", "WINDOWS_USER", "USERNAME"):
            v = os.environ.get(var)
            if v and v not in candidates:
                candidates.append(v)
        # Also try whatever's in /mnt/c/Users — operator's username
        # is usually the only non-system entry.
        try:
            for entry in os.listdir("/mnt/c/Users"):
                if entry not in ("Default", "Default User", "Public",
                                  "All Users", "desktop.ini",
                                  "WsiAccount") and entry not in candidates:
                    candidates.append(entry)
        except OSError:
            pass
        for u in candidates:
            yield f"/mnt/c/Users/{u}/.smirror-deploy.env"

    # 5. Repo-root .smirror-deploy.env (project-local).
    # Walk up from this file's directory looking for a `.git` sibling.
    here = os.path.dirname(os.path.abspath(__file__))
    for _ in range(5):
        if os.path.exists(os.path.join(here, ".git")):
            yield os.path.join(here, ".smirror-deploy.env")
            break
        parent = os.path.dirname(here)
        if parent == here:
            break
        here = parent


def _parse_env_file(path):
    """Parse a bash-sourceable env file. Returns dict of {var: value}.

    Tolerates:
      - Leading `export ` (stripped)
      - Comments (`# ...` lines and trailing `# ...` if quoted properly)
      - Blank lines
      - Single- and double-quoted values

    Does NOT support:
      - Variable expansion (`$OTHER_VAR` is treated literally)
      - Multi-line values
      - Backtick command substitution
    """
    out = {}
    try:
        with open(path, "r", encoding="utf-8") as f:
            text = f.read()
    except OSError:
        return out

    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        # Strip leading `export `
        if line.startswith("export "):
            line = line[len("export "):].strip()
        if "=" not in line:
            continue
        key, _, value = line.partition("=")
        key = key.strip()
        value = value.strip()
        if not key:
            continue
        # Strip surrounding quotes if both ends match
        if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
            value = value[1:-1]
        out[key] = value
    return out


def load_deploy_env(verbose=False):
    """Discover + load `.smirror-deploy.env`. Returns (path, vars_loaded).

    `vars_loaded` is the number of NEW variables added to os.environ
    (variables already set are not overwritten — the operator's
    manual env always wins). `path` is None if no file was found.

    Set `verbose=True` to print discovery diagnostics to stderr.
    """
    for path in _candidate_paths():
        if not path or not os.path.isfile(path):
            if verbose:
                sys.stderr.write(f"  (skip {path}: not found)\n")
            continue
        if verbose:
            sys.stderr.write(f"  Loading {path}\n")
        env_vars = _parse_env_file(path)
        loaded = 0
        for key, value in env_vars.items():
            if key in os.environ:
                if verbose:
                    sys.stderr.write(f"    {key}: already set, kept operator value\n")
                continue
            os.environ[key] = value
            loaded += 1
            if verbose:
                # Mask the value: show first 4 chars + length
                masked = (value[:4] + "..." + f"({len(value)} chars)") if len(value) > 8 else "<short>"
                sys.stderr.write(f"    {key} = {masked}\n")
        return path, loaded
    if verbose:
        sys.stderr.write("  No .smirror-deploy.env found in any candidate location.\n")
    return None, 0


def ensure_database_url(verbose=False):
    """Convenience wrapper: ensure DATABASE_URL is set, auto-loading
    from .smirror-deploy.env if necessary. Returns the resolved value
    or None if it couldn't be found anywhere."""
    if "DATABASE_URL" in os.environ and os.environ["DATABASE_URL"]:
        return os.environ["DATABASE_URL"]
    load_deploy_env(verbose=verbose)
    return os.environ.get("DATABASE_URL") or None
