Primary author: Raveh Neeman

Development assistance and technical sparring:
- Anthropic Claude
- OpenAI ChatGPT

## Third-Party Dependencies

### Compiled into binary (Go modules)

| Package | License | URL |
|---------|---------|-----|
| fsnotify/fsnotify | BSD 3-Clause | https://github.com/fsnotify/fsnotify |
| git-pkgs/gitignore | MIT | https://github.com/git-pkgs/gitignore |
| go.yaml.in/yaml/v3 | MIT + Apache 2.0 | https://github.com/go-yaml/yaml |
| mattn/go-sqlite3 | MIT | https://github.com/mattn/go-sqlite3 |
| mattn/go-isatty | MIT | https://github.com/mattn/go-isatty |
| golang.org/x/sys | BSD 3-Clause | https://pkg.go.dev/golang.org/x/sys |

### Embedded C (via mattn/go-sqlite3, statically linked at build time)

| Component | License | URL |
|-----------|---------|-----|
| SQLite | Public Domain | https://www.sqlite.org/copyright.html |

### Runtime dependency (external process)

| Tool | License | URL |
|------|---------|-----|
| rclone | MIT | https://rclone.org/licence/ |

rclone is invoked as an external subprocess. It is not compiled into SelectiveMirror
and must be installed separately by the user.

All dependencies use permissive open-source licenses (MIT, BSD, Apache 2.0).
See THIRD-PARTY-LICENSES.txt for full texts.

---

No support, maintenance, or liability obligations are assumed.
