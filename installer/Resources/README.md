# installer/Resources

Static assets bundled into the WiX MSI build.

## Files

| File | Purpose | Source of truth |
|---|---|---|
| `license.rtf` | License text shown by `LicenseAgreementDlg` during install | hand-authored |
| `sm_icon.png` | Source product icon, 1024×1024 RGBA | hand-authored |
| `sm_icon.ico` | Multi-size product icon for `ARPPRODUCTICON` (Add/Remove Programs / Installed apps) | **generated from `sm_icon.png`** — regenerate any time the PNG changes |
| `WelcomeDlgBmp.bmp` | Welcome/Exit dialog left-side graphic, 493×312, 24-bit BMP. Bound to `WixUIDialogBmp` in `Package.wxs`. | **generated from `sm_icon.png`** |
| `BannerBmp.bmp` | Inner-dialog top banner (License / InstallDir / Tier / Progress), 493×58, 24-bit BMP. Bound to `WixUIBannerBmp` in `Package.wxs`. | **generated from `sm_icon.png`** |

## Regenerating `sm_icon.ico`

The ICO is committed so MSI builds don't need PIL at build time. Regenerate when the PNG changes:

```bash
python -c "
from PIL import Image
img = Image.open('installer/Resources/sm_icon.png')
if img.mode != 'RGBA':
    img = img.convert('RGBA')
img.save('installer/Resources/sm_icon.ico', format='ICO',
    sizes=[(16,16),(20,20),(24,24),(32,32),(40,40),(48,48),(64,64),(96,96),(128,128),(256,256)])
"
```

The 10-size set covers every Windows DPI scenario (small icons in Explorer, large icons in Start menu, high-DPI displays at 256×256). Windows Installer picks the appropriate size at display time.

## Regenerating `WelcomeDlgBmp.bmp` and `BannerBmp.bmp`

The WiX UI extension expects 24-bit BMPs at fixed dimensions (493×312 for
the Welcome/Exit dialog graphic, 493×58 for the inner-dialog banner). The
canvas is white; the logo is positioned so the WiX-painted text overlay
doesn't clobber it (right ~313 px on Welcome, left ~373 px on the banner
are reserved for text).

Regenerate when `sm_icon.png` changes:

```bash
python <<'PY'
from PIL import Image
logo = Image.open('installer/Resources/sm_icon.png').convert('RGBA')

# Welcome dialog: 493x312, logo centered in left ~180 px graphic zone.
welcome = Image.new('RGB', (493, 312), (255, 255, 255))
sz = 150
img = logo.resize((sz, sz), Image.LANCZOS)
welcome.paste(img, ((180 - sz) // 2, (312 - sz) // 2), mask=img.split()[3])
welcome.save('installer/Resources/WelcomeDlgBmp.bmp', format='BMP')

# Banner: 493x58, logo right-aligned, vertically centered.
banner = Image.new('RGB', (493, 58), (255, 255, 255))
sz = 48
img = logo.resize((sz, sz), Image.LANCZOS)
banner.paste(img, (493 - sz - 14, (58 - sz) // 2), mask=img.split()[3])
banner.save('installer/Resources/BannerBmp.bmp', format='BMP')
PY
```

## Where the icon shows up

- **Add/Remove Programs / Installed apps** (Settings → Apps → Installed apps): icon next to "SelectiveMirror" entry.
- **Programs and Features** (legacy Control Panel): same.
- **MSI file properties** (right-click `SelectiveMirror.msi` → Properties): MSI icon.
- **Setup wizard Welcome / Exit dialogs**: the 493×312 graphic (`WelcomeDlgBmp.bmp`).
- **Setup wizard inner dialogs** (License, InstallDir, Telemetry tier, Progress, etc.): the 493×58 top banner (`BannerBmp.bmp`).

The icon is **not** embedded in `smirror.exe` itself — that's a Go-build-time concern (would require `goversioninfo` or `rsrc` to embed an `.ico` resource into the PE binary). Out of scope for the MSI wiring; tracked as a v1.0.x nice-to-have if anyone files for it.
