# installer/Resources

Static assets bundled into the WiX MSI build.

## Files

| File | Purpose | Source of truth |
|---|---|---|
| `license.rtf` | License text shown by `LicenseAgreementDlg` during install | hand-authored |
| `sm_icon.png` | Source product icon, 1024×1024 RGB | hand-authored |
| `sm_icon.ico` | Multi-size product icon for `ARPPRODUCTICON` (Add/Remove Programs / Installed apps) | **generated from `sm_icon.png`** — regenerate any time the PNG changes |

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

## Where the icon shows up

- **Add/Remove Programs / Installed apps** (Settings → Apps → Installed apps): icon next to "SelectiveMirror" entry.
- **Programs and Features** (legacy Control Panel): same.
- **MSI file properties** (right-click `SelectiveMirror.msi` → Properties): MSI icon.

The icon is **not** embedded in `smirror.exe` itself — that's a Go-build-time concern (would require `goversioninfo` or `rsrc` to embed an `.ico` resource into the PE binary). Out of scope for the MSI wiring; tracked as a v1.0.x nice-to-have if anyone files for it.
