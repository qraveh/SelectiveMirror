//go:build windows

// Package main embeds a Windows PE VERSIONINFO resource so that
// `smirror.exe → Properties → Details` shows CompanyName, ProductName,
// FileVersion, OriginalFilename, and copyright. This identifies the
// binary as a published, non-anonymous build (R-24).
//
// Why this is in the source tree
//
// Microsoft Defender's ML classifier (`Trojan:Win32/Wacatac.B!ml`)
// flagged the unsigned `smirror.exe` shipped with the v1.0.59 MSI on
// first download. One of the strongest ML signals against us was
// "binary has no PE version info" — anonymous, fingerprint-less Go
// builds look statistically like packed/obfuscated malware to the
// classifier. Embedding VERSIONINFO neutralizes that signal without
// requiring an Authenticode certificate (which is the longer-term fix).
//
// How regeneration works
//
// `cmd/smirror/versioninfo.json` is the source of truth. To regenerate
// `cmd/smirror/resource_windows.syso` after editing the JSON (e.g. on
// a version bump), run:
//
//	go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
//	go generate ./cmd/smirror/
//
// Go's linker picks up any `*_windows.syso` in the main package on
// Windows builds automatically — no `.goreleaser.yaml` change is needed
// to wire it in. CI builds (GoReleaser) also regenerate the syso with
// the actual tag version via the `before.hooks` step.
//
// Reference: docs/release-maturity.md (R-24);
//            docs/operations/wdsi-fp-submission-v1.0.59.md.

//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -o resource_windows.syso versioninfo.json

package main
