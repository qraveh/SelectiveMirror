Primary author: Raveh Neeman

Development assistance and technical sparring:
- Anthropic Claude
- OpenAI ChatGPT

## Third-Party Dependencies

### Compiled into binary (Go modules)

| Package | License | URL |
|---------|---------|-----|
| fsnotify/fsnotify | BSD 3-Clause | https://github.com/fsnotify/fsnotify |
| sabhiram/go-gitignore | MIT | https://github.com/sabhiram/go-gitignore |
| gopkg.in/yaml.v3 | MIT + Apache 2.0 | https://github.com/go-yaml/yaml |
| modernc.org/sqlite | BSD 3-Clause | https://gitlab.com/cznic/sqlite |
| golang.org/x/sys | BSD 3-Clause | https://pkg.go.dev/golang.org/x/sys |
| dustin/go-humanize | MIT | https://github.com/dustin/go-humanize |
| google/uuid | BSD 3-Clause | https://github.com/google/uuid |
| mattn/go-isatty | MIT | https://github.com/mattn/go-isatty |
| ncruces/go-strftime | MIT | https://github.com/ncruces/go-strftime |
| modernc.org/libc | BSD 3-Clause | https://gitlab.com/cznic/libc |
| modernc.org/mathutil | BSD 3-Clause | https://gitlab.com/cznic/mathutil |
| modernc.org/memory | BSD 3-Clause | https://gitlab.com/cznic/memory |
| remyoudompheng/bigfft | BSD 3-Clause | https://github.com/remyoudompheng/bigfft |

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
