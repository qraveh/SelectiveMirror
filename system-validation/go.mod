module systemval

go 1.26.1

// Test-only dependency for the installer→runtime handoff E2E test
// (system-validation/installer_handoff_seam_e2e_test.go). The test
// pre-populates a state.db file in the SM-216 shape (telemetry_tier
// = "standard", install_id missing) and reads it back after the
// daemon's recovery branch has run, all without importing
// internal/state. Pinned to the parent module's go-sqlite3 version
// so the CGo build is consistent across both modules.
require github.com/mattn/go-sqlite3 v1.14.42
