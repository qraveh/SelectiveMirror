SelectiveMirror v0.1.0
======================

Real-time selective file synchronization for Windows.
Watches local directories and mirrors changes to any rclone-supported
backend (Google Drive, S3, Dropbox, OneDrive, SFTP, and 70+ others).

Prerequisites
-------------
  - rclone v1.73+ (install with: winget install Rclone.Rclone)
  - An rclone remote configured via: rclone config

Quick Start
-----------
  1. Configure a remote:        rclone config
  2. Copy config.example.yaml to %USERPROFILE%\.selectivemirror\config.yaml
  3. Edit config.yaml with your mirrors and remote
  4. Run diagnostics:           smirror test-mirrors
  5. Preview what would sync:   smirror dry-run
  6. Start mirroring:           smirror start

Commands
--------
  smirror start              Start foreground watcher
  smirror sync-now           Immediate full sync
  smirror dry-run            Show what would sync
  smirror status             Show sync status and metrics
  smirror test-mirrors       Run all diagnostics and verify sync state
  smirror list-filters       Show effective filter rules
  smirror explain <m> <f>    Show include/exclude status for a file
  smirror project-stats      Show file counts and line counts
  smirror service <action>   Windows Service: install, uninstall, start, stop
  smirror version            Show version

Configuration
-------------
  Default config: %USERPROFILE%\.selectivemirror\config.yaml
  Override with:  smirror --config path\to\config.yaml <command>
  See config.example.yaml for a full annotated example.

More Information
----------------
  GitHub:  https://github.com/qraveh/SelectiveMirror
  License: MIT (see LICENSE file)
