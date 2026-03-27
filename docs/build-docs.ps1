# build-docs.ps1 — Build PDF manuals from Markdown sources
# Prerequisites: pandoc, tectonic (or another LaTeX engine)
# Install: winget install Pandoc.Pandoc
#          winget install Tectonic.Tectonic

$ErrorActionPreference = "Stop"
$docsDir = $PSScriptRoot

$manuals = @(
    @{ Source = "installation-manual.md"; Output = "Installation Manual.pdf" },
    @{ Source = "user-manual.md";         Output = "User Manual.pdf" },
    @{ Source = "developer-manual.md";    Output = "Developer Manual.pdf" }
)

# Check prerequisites
$pandoc = Get-Command pandoc -ErrorAction SilentlyContinue
if (-not $pandoc) {
    Write-Error "pandoc not found. Install: winget install Pandoc.Pandoc"
    exit 1
}

$tectonic = Get-Command tectonic -ErrorAction SilentlyContinue
$pdfEngine = if ($tectonic) { "tectonic" } else { "xelatex" }

Write-Host "Building PDFs with pandoc + $pdfEngine" -ForegroundColor Cyan
Write-Host ""

$built = 0
$failed = 0

foreach ($m in $manuals) {
    $src = Join-Path $docsDir $m.Source
    $out = Join-Path $docsDir $m.Output

    if (-not (Test-Path $src)) {
        Write-Host "SKIP: $($m.Source) (not found)" -ForegroundColor Yellow
        continue
    }

    Write-Host "Building: $($m.Output)..." -NoNewline

    try {
        & pandoc $src -o $out `
            --pdf-engine=$pdfEngine `
            -V geometry:margin=1in `
            -V fontsize=11pt `
            --toc `
            --toc-depth=2 `
            --highlight-style=tango `
            -V colorlinks=true `
            -V urlcolor=blue `
            -V toccolor=black `
            2>&1

        if ($LASTEXITCODE -eq 0 -and (Test-Path $out)) {
            $size = (Get-Item $out).Length / 1KB
            Write-Host " OK ({0:N0} KB)" -f $size -ForegroundColor Green
            $built++
        } else {
            Write-Host " FAILED (exit $LASTEXITCODE)" -ForegroundColor Red
            $failed++
        }
    } catch {
        Write-Host " ERROR: $_" -ForegroundColor Red
        $failed++
    }
}

Write-Host ""
Write-Host "Built: $built  Failed: $failed" -ForegroundColor $(if ($failed -gt 0) { "Red" } else { "Green" })
