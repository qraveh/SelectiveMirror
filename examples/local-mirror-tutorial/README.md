# SelectiveMirror — Local-Mirror Tutorial

A hands-on tour of `smirror`. The tutorial uses rclone's **local-filesystem
backend** as a stand-in for a real cloud remote, so you can run it on any
Windows machine without a cloud account, network access, or credentials.

All files this tutorial creates live in **one workspace folder**. Delete the
folder when you're done and you're back to zero.

> **What you'll learn**
>
> - **Part 1 (≈5 min)** — configure a mirror, see what `smirror` will and
>   won't sync (`dry-run`), run the sync, edit a file and watch the change
>   propagate.
> - **Part 2 (≈15 min, optional)** — diagnose individual files (`explain`),
>   see what happens on delete (quarantine policy), detect drift (`verify`),
>   read live stats, run in the background, graduate to a real cloud
>   backend.

---

## Prerequisites

You need:

- `smirror.exe` on your PATH — verify with `smirror version`
- `rclone.exe` on your PATH (v1.73+) — verify with `rclone version`
- A terminal — `cmd.exe` works fine; the commands below use cmd syntax

The tutorial does not need administrator privileges, network access, or any
cloud account.

> The commands below are written for `cmd.exe`. If you use PowerShell,
> `%CD%` becomes `$PWD`, `dir /S` becomes `Get-ChildItem -Recurse`, etc.
> The smirror and rclone commands themselves are identical in either shell.

---

## Part 1 — The 5-minute walkthrough

### Step 1 — Create the workspace

Pick a location for the tutorial workspace. Anywhere will do:

```cmd
mkdir %USERPROFILE%\smirror-tutorial
cd %USERPROFILE%\smirror-tutorial
mkdir remote
```

Copy the source-template directory into your workspace as `source`. Pick
the line that matches how you got smirror:

```cmd
:: If you installed via the MSI (recommended path):
xcopy /E /I "%ProgramFiles%\SelectiveMirror\examples\local-mirror-tutorial\source-template" source

:: ...or, if you have the SelectiveMirror source repo checked out:
xcopy /E /I "<smirror-repo>\examples\local-mirror-tutorial\source-template" source
```

> **Have only the portable ZIP and no source?** The portable ZIP carries
> `smirror.exe` but not the tutorial fixture. Either clone the source repo
> (`git clone https://github.com/qraveh/SelectiveMirror.git`) and use the
> second line above, or create the files yourself — they're listed in the
> [Source-template reference](#source-template-reference) section below.
> The contents are trivial; what matters is the filenames.

Your workspace now looks like:

```
%USERPROFILE%\smirror-tutorial\
├── source\
│   ├── .syncignore
│   ├── .dont_mirror_hidden.txt
│   ├── dont_mirror_me.log
│   ├── dont_mirror_me.tmp
│   ├── dont_mirror_this_dir\
│   │   └── stuff.txt
│   ├── file_a.txt
│   ├── file_b.txt
│   ├── mirror_me_1.txt
│   ├── mirror_me_2.txt
│   └── mirror_this_dir\
│       ├── nested_1.txt
│       └── nested_2.txt
└── remote\        (empty for now)
```

The names tell the story: anything called `mirror_me_*` or `mirror_this_dir/*`
should appear on the remote after we sync; anything called `dont_mirror_*`
should not. The `.syncignore` file is the filter that makes that happen.

### Step 2 — Configure a local-filesystem rclone remote

`smirror` always talks to its destination via `rclone`. For this tutorial,
the "remote" is just the `remote\` folder you just created.

```cmd
rclone config
```

Walk through the prompts:

| Prompt | Answer |
|---|---|
| `n/s/q>` | `n` (new remote) |
| `name>` | `local-tutorial` |
| `Storage>` | The number for `Local Disk` (usually `Local` or `local`) |
| Everything else | Accept the default by pressing Enter |
| `y/e/d>` | `y` (yes, this is OK) |
| `e/n/d/r/c/s/q>` | `q` (quit config) |

Verify the remote is registered:

```cmd
rclone listremotes
```

The output should include `local-tutorial:`.

### Step 3 — Register the mirror and preview the plan

Tell smirror to mirror the `source` folder. The `-dest` is the **parent**
folder on the remote where the mirror lands — smirror creates a
subdirectory named after the mirror (`source`) inside it. So pass
`local-tutorial:%CD%\remote` (not `\remote\source`) and your files will
end up at `remote\source\…`:

```cmd
smirror addmirror %CD%\source -dest local-tutorial:%CD%\remote
```

This writes a mirror entry into your `~/.selectivemirror/config.yaml`
(the file is created if it doesn't exist). The tutorial's
[Cleanup](#cleanup) section reverses this.

Now ask smirror what it *would* sync — without copying anything yet:

```cmd
smirror dry-run source
```

(`source` is the mirror name — smirror takes the last segment of the local
path by default.)

You should see something like:

```
=== Dry run: source ===
Source: ...\source
Destination: local-tutorial:...\remote\source
Running: rclone copy ... --filter-from ... --dry-run ...

NOTICE: file_a.txt: Skipped copy as --dry-run is set
NOTICE: file_b.txt: Skipped copy as --dry-run is set
NOTICE: mirror_me_1.txt: Skipped copy as --dry-run is set
NOTICE: mirror_me_2.txt: Skipped copy as --dry-run is set
NOTICE: mirror_this_dir/nested_1.txt: Skipped copy as --dry-run is set
NOTICE: mirror_this_dir/nested_2.txt: Skipped copy as --dry-run is set

Transferred:    6 / 6, 100%
```

What's *not* in that list is what the filter excluded — the
`dont_mirror_*` files and the `dont_mirror_this_dir/` folder. The filter
worked without smirror ever touching a byte on the remote.

> **This is smirror's main differentiator from a naive `rclone sync`:** it
> tells you exactly what it will and will not do, before it does anything.

### Step 4 — Sync, edit, sync again

Run the actual sync:

```cmd
smirror sync-now source
```

Look at the remote:

```cmd
dir /S /B remote
```

You should see:

```
remote\source\file_a.txt
remote\source\file_b.txt
remote\source\mirror_me_1.txt
remote\source\mirror_me_2.txt
remote\source\mirror_this_dir\nested_1.txt
remote\source\mirror_this_dir\nested_2.txt
```

The `dont_mirror_*` files and `dont_mirror_this_dir/` are absent, exactly
as the dry-run predicted.

Now edit one of the synced files:

```cmd
echo additional line>> source\mirror_me_1.txt
smirror sync-now source
type remote\source\mirror_me_1.txt
```

The remote copy has your edit.

In real use, you'd run `smirror start` and the watcher would pick up edits
automatically — for the tutorial we're driving it manually with `sync-now`
so the cause-and-effect is visible.

**That's Part 1.** You've configured a mirror, seen the filter at work,
and synced edits. If you stop here, jump to [Cleanup](#cleanup).

---

## Part 2 — Deeper dive (optional)

### Step 5 — `smirror explain` — per-file diagnosis

`explain` answers the question "is this file going to sync, and if not,
why not?". It works on both included and excluded files:

```cmd
smirror explain source mirror_me_1.txt
smirror explain source dont_mirror_me.tmp
smirror explain source dont_mirror_this_dir\stuff.txt
```

For each file, `explain` reports:

- Whether it's **included** or **excluded**
- If excluded, which `.syncignore` rule matched
- Its current **sync state** (synced / pending / out of date)

This is the canonical "why isn't my file syncing?" tool.

#### Try toggling a filter

`file_a.txt` is currently included. Add it to `.syncignore`:

```cmd
echo file_a.txt>> source\.syncignore
smirror explain source file_a.txt
```

You should see it now reports **excluded**, matched by the rule
`file_a.txt`. Run dry-run again — `file_a.txt` is no longer in the
WOULD-COPY list.

Remove your line from `.syncignore` (open it in a text editor) and rerun
`smirror explain source file_a.txt` — it's back to **included**.

### Step 6 — Quarantine on delete

By default, smirror **does not propagate local deletes** to the remote.
Instead, the remote copy is moved into a `.quarantine/` folder, giving you
a 30-day recovery window. This protects against accidental `rm -rf` or
editor cleanup gone wrong from nuking your backup.

```cmd
del source\mirror_me_2.txt
smirror sync-now source
dir remote\source\.quarantine
```

The remote copy is now in `.quarantine/` rather than gone. The filename
includes a timestamp suffix so multiple quarantines of the same path
don't collide — something like
`mirror_me_2.txt.20260525T231826Z.597108600`. If the delete was a
mistake, `move` the file back to `remote\source\` (drop the timestamp
suffix from the destination name).

If you do want strict 1:1 deletion mirroring, set `delete_policy: delete`
in your config. See [docs/](../../docs/) for the full options and tradeoffs.

### Step 7 — Drift detection (`smirror verify`)

What if something *other* than smirror has touched the remote? Maybe
another tool wrote a file there, or a previous run left orphans behind.
`verify` reports drift in both directions.

Add a "ghost" file directly to the remote:

```cmd
echo this came from somewhere else > remote\source\ghost_file.txt
smirror verify source
```

`verify` reports `ghost_file.txt` as an orphan on the remote — it has no
counterpart in the source. Drift is surfaced in plain text; you decide
what to do about it.

### Step 8 — Live stats

```cmd
smirror status source
smirror project-stats source
```

`status` shows live counters (files synced, queue depth, last sync time,
errors). `project-stats` shows what each mirror holds (file count, total
size, line count).

### Step 9 — Background mode (per-user Scheduled Task)

`smirror start` runs in the foreground. For "keep mirroring after I close
the terminal", smirror registers a per-user Scheduled Task — no admin
elevation needed:

```cmd
smirror task install
smirror task status
```

The watcher is now running in the background. Stop it with
`smirror task stop`; remove the task entirely with `smirror task uninstall`
(the cleanup section will do this).

There is also `smirror service install` for system-wide LocalSystem mode,
which needs admin and an admin-owned config — see the README and
SECURITY.md for the trust model. For a single-user laptop, `task` is the
right choice.

### Step 10 — Graduate to a real cloud backend

Once you've seen smirror work on a local-fs remote, the move to a real
cloud backend is two commands:

```cmd
:: 1. Create a new rclone remote pointing at your cloud backend
rclone config
::    e.g. choose Google Drive, S3, Dropbox, OneDrive, etc.
::    rclone walks you through the auth flow.

:: 2. Swap the destination on your existing mirror
smirror unmirror %CD%\source
smirror addmirror %CD%\source -dest <your-real-remote>:Backup/source
smirror sync-now source
```

Your `.syncignore` file travels with the source folder — no changes
needed. Everything you learned in this tutorial transfers directly.

---

## Cleanup

These commands undo everything the tutorial created:

```cmd
:: If you ran Step 9 (background mode)
smirror task uninstall

:: Remove the mirror entry from your smirror config
smirror unmirror %CD%\source

:: Remove the rclone remote
rclone config delete local-tutorial

:: Delete the workspace folder
cd %USERPROFILE%
rmdir /S /Q %USERPROFILE%\smirror-tutorial
```

That's everything. Your `~/.selectivemirror/config.yaml` and rclone config
are back to whatever state they were in before you started.

---

## Source-template reference

If you'd rather create the source files yourself than copy from the repo,
here's the full layout. File contents are trivial — exactly what each
file's name says, e.g. `mirror_me_1.txt` contains the literal text
`content of mirror_me_1.txt`.

```
source/
├── .syncignore                    (filter rules — see below)
├── .dont_mirror_hidden.txt        (excluded — matches `.dont_mirror_hidden.txt`)
├── dont_mirror_me.log             (excluded — matches `*.log`)
├── dont_mirror_me.tmp             (excluded — matches `*.tmp`)
├── dont_mirror_this_dir\
│   └── stuff.txt                  (excluded — matches `/dont_mirror_this_dir/`)
├── file_a.txt                     (included; neutral name — used in Step 5 toggle exercise)
├── file_b.txt                     (included; neutral name)
├── mirror_me_1.txt                (included)
├── mirror_me_2.txt                (included)
└── mirror_this_dir\
    ├── nested_1.txt               (included)
    └── nested_2.txt               (included)
```

`.syncignore` content:

```
# By extension
*.tmp
*.log

# A specific hidden (dotfile) file
.dont_mirror_hidden.txt

# A specific directory and everything in it
/dont_mirror_this_dir/
```

---

## Naming convention used by this tutorial

The fixture files use deliberately self-describing names:

- **`mirror_*`** — files and directories the filter is designed to **include**. Their fixed role is reflected in the name.
- **`dont_mirror_*`** — files and directories the filter is designed to **exclude**. Their fixed role is reflected in the name.
- **`file_a`, `file_b`** — neutral names. Step 5's toggle exercise changes whether these are included; the names don't predetermine a decision.

The split lets the always-stable files self-document their role while still leaving room for an exercise where the reader changes their mind.

---

## Where to go next

- [`smirror --help`](../../cmd/smirror/) — full command reference
- [`config.example.yaml`](../../config.example.yaml) — annotated config file
- [`docs/PRIVACY.md`](../../docs/PRIVACY.md) — telemetry posture (opt-in, default off)
- [`SECURITY.md`](../../SECURITY.md) — trust model for task vs service mode, hook security, signing
- [`CONTRIBUTING.md`](../../CONTRIBUTING.md) — if you want to contribute
