SelectiveMirror
===============
Real-time selective file synchronization engine for Windows.
Version 1.0.0 | MIT License | github.com/qraveh/SelectiveMirror

Author: Raveh (raveh@qodeh.com)


WHAT IT DOES
------------

SelectiveMirror is a real-time file synchronization engine for Windows
that watches local project directories and mirrors changes to any of
rclone's 70+ supported cloud backends (Google Drive, S3, Dropbox, SFTP,
and more). It detects file creates, modifications, renames, and deletes
via the Windows ReadDirectoryChangesW API, debounces rapid edits, and
syncs only changed files using MD5 checksums -- eliminating redundant
uploads. Each project can define .syncignore rules (gitignore syntax)
for excluding build artifacts, node_modules, or sensitive files, with
hot-reload on edit.

The engine runs as a lightweight background process (~15MB RAM, <1% CPU
idle) with configurable concurrent workers, per-file locking to prevent
race conditions, and a 5-minute timeout per rclone operation. Three
delete policies (ignore, mirror, quarantine) control how local deletions
propagate to remotes. A built-in state database (SQLite) tracks sync
history, checksums, and rclone exit codes for every file. Diagnostics
include "smirror doctor" (12-point self-test), "smirror verify" (drift
detection with remote), "smirror stats" (sync metrics), and
"smirror report-bug" (automated diagnostic collection).


SYSTEM REQUIREMENTS
-------------------

  - Windows 10 or 11 (amd64)
  - rclone v1.73 or later (https://rclone.org/downloads/)
    Install: winget install Rclone.Rclone

rclone is the engine behind all file transfers. SelectiveMirror
invokes it as a subprocess for each sync operation. It supports 70+
storage backends: Google Drive, Amazon S3, Dropbox, OneDrive, SFTP,
local filesystem, Backblaze B2, Cloudflare R2, and many more. You
configure backends once with "rclone config" and SelectiveMirror
uses them transparently.


QUICK START
-----------

  1. Install rclone and configure a remote:
       winget install Rclone.Rclone
       rclone config    (follow prompts for your cloud storage)

  2. Install SelectiveMirror (MSI or extract smirror.exe to PATH)

  3. Edit config: %USERPROFILE%\.selectivemirror\config.yaml
       projects:
         - name: MyProject
           local_path: C:\Projects\MyProject
           remote: gdrive:backup/MyProject

  4. Validate:
       smirror validate
       smirror dry-run

  5. Start watching:
       smirror start


COMMANDS
--------

  start                  Start the file watcher (foreground)
  sync-now [project]     Trigger immediate sync
  dry-run [project]      Show what would be synced
  status                 Show sync status and metrics
  validate               Check config and rclone connectivity
  list-filters [project] Show effective filter rules
  explain <proj> <path>  Explain why a file is included/excluded
  doctor                 Run 12-point self-test diagnostics
  verify [project]       Compare local vs remote (drift detection)
  stats                  Show file/line counts across projects
  report-bug [--stdout]  Generate diagnostic report for bug filing
  version                Show version


PDF MANUALS
-----------

  Installation Manual.pdf  - Setup, rclone config, first run
  User Manual.pdf          - Full config reference, commands, backends
  Developer Manual.pdf     - Architecture, testing, contributing


BUG REPORTS
-----------

  smirror report-bug --stdout     (generates diagnostic report)
  smirror report-bug --open       (opens GitHub issue form in browser)

  Or file directly at:
  https://github.com/qraveh/SelectiveMirror/issues


LICENSE
-------

MIT License. See LICENSE file for details.
Third-party licenses: see THIRD-PARTY-LICENSES.txt
