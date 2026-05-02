package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signWithMaster computes the per-version derived key the same way the
// server does (HMAC(version, master_key)), and returns its hex form so
// it can be assigned to buildKey for tests. Mirrors what CI does at
// build time but in-process.
func signWithMaster(t *testing.T, masterKey, version string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(masterKey))
	mac.Write([]byte(version))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestContribute_NoBuildKey(t *testing.T) {
	saved := buildKey
	buildKey = ""
	t.Cleanup(func() { buildKey = saved })

	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: "http://example.invalid"})
	if !errors.Is(err, ErrNoBuildKey) {
		t.Errorf("got %v; want ErrNoBuildKey", err)
	}
}

func TestContribute_RequiresEventKind(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{}, ContributeOptions{Endpoint: "http://example.invalid"})
	if err == nil || !strings.Contains(err.Error(), "event_kind") {
		t.Errorf("got %v; want error mentioning event_kind", err)
	}
}

func TestContribute_RequiresVersion(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	err := Contribute(context.Background(), "",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: "http://example.invalid"})
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("got %v; want error mentioning version", err)
	}
}

// TestContribute_HappyPath asserts that:
//   - The wire body contains payload, claimed_version, claimed_hmac_hex.
//   - The HMAC over canonical(payload-without-event_kind) verifies
//     against a server-side recompute (i.e., the canonical-bytes contract
//     between client and server is round-trip-clean).
//   - A 2xx with {"ok": true} returns nil.
func TestContribute_HappyPath(t *testing.T) {
	const ver = "0.0.0-happy"
	const masterKey = "test-master-key-for-happy-path"

	saved := buildKey
	buildKey = signWithMaster(t, masterKey, ver)
	t.Cleanup(func() { buildKey = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/contribute" {
			t.Errorf("got path %q; want /v1/contribute", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("got method %q; want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q; want application/json", r.Header.Get("Content-Type"))
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var wire struct {
			Payload      map[string]any `json:"payload"`
			Version      string         `json:"claimed_version"`
			HMACHex      string         `json:"claimed_hmac_hex"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if wire.Version != ver {
			t.Errorf("claimed_version = %q; want %q", wire.Version, ver)
		}
		if wire.Payload == nil {
			t.Fatal("payload is nil")
		}
		if wire.Payload["event_kind"] != "first_seen" {
			t.Errorf("payload.event_kind = %v; want first_seen", wire.Payload["event_kind"])
		}

		// Server-side recompute. Strip event_kind + version_hmac, then
		// canonicalize, then HMAC with the per-version derived key.
		signing := make(map[string]any, len(wire.Payload))
		for k, v := range wire.Payload {
			if k == "event_kind" || k == "version_hmac" {
				continue
			}
			signing[k] = v
		}
		canonical, err := CanonicalJSON(signing)
		if err != nil {
			t.Fatalf("server canonical: %v", err)
		}
		mac := hmac.New(sha256.New, []byte(signWithMaster(t, masterKey, ver)))
		// signWithMaster gives us a hex string; we need to decode it
		// back to bytes for the HMAC key. Wait — actually, the build
		// key IS that derived key; the server's verify_versioned_hmac
		// derives it the same way and uses raw bytes. Let me redo:
		_ = mac
		derivedHex := signWithMaster(t, masterKey, ver)
		derived, err := hex.DecodeString(derivedHex)
		if err != nil {
			t.Fatalf("decode derived: %v", err)
		}
		mac2 := hmac.New(sha256.New, derived)
		mac2.Write([]byte(canonical))
		expectedHex := hex.EncodeToString(mac2.Sum(nil))
		if wire.HMACHex != expectedHex {
			t.Errorf("HMAC mismatch:\n  client sent: %s\n  server got : %s", wire.HMACHex, expectedHex)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	err := Contribute(context.Background(), ver,
		map[string]any{
			"event_kind":     "first_seen",
			"client_version": ver,
			"install_method": "msi",
			"os_family":      "windows",
		},
		ContributeOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("Contribute returned %v; want nil", err)
	}
}

func TestContribute_ServerReturnsRejected(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": false, "error": "rejected"}`))
	}))
	defer srv.Close()

	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: srv.URL})
	if !errors.Is(err, ErrRejected) {
		t.Errorf("got %v; want ErrRejected", err)
	}
}

func TestContribute_ServerReturnsSchemaViolation(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": false, "error": "schema_violation:bad_enum"}`))
	}))
	defer srv.Close()

	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: srv.URL})
	if !errors.Is(err, ErrSchemaViolation) {
		t.Errorf("got %v; want ErrSchemaViolation wrapper", err)
	}
	if !strings.Contains(err.Error(), "bad_enum") {
		t.Errorf("error %q does not contain server's detail string", err.Error())
	}
}

func TestContribute_ServerReturnsUnknownEvent(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": false, "error": "unknown_event"}`))
	}))
	defer srv.Close()

	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: srv.URL})
	if !errors.Is(err, ErrUnknownEvent) {
		t.Errorf("got %v; want ErrUnknownEvent", err)
	}
}

func TestContribute_HTTP410ReturnsNetworkErr(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"code":"endpoint_retired"}`))
	}))
	defer srv.Close()

	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: srv.URL})
	if !errors.Is(err, ErrNetwork) {
		t.Errorf("got %v; want ErrNetwork", err)
	}
	if !strings.Contains(err.Error(), "410") {
		t.Errorf("error %q does not mention status 410", err.Error())
	}
}

func TestContribute_HTTP500ReturnsNetworkErr(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: srv.URL})
	if !errors.Is(err, ErrNetwork) {
		t.Errorf("got %v; want ErrNetwork", err)
	}
}

func TestContribute_MalformedResponseBody(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`<html>not JSON</html>`))
	}))
	defer srv.Close()

	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: srv.URL})
	if !errors.Is(err, ErrServerMalformed) {
		t.Errorf("got %v; want ErrServerMalformed", err)
	}
}

func TestContribute_RespectsContextDeadline(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stall longer than the test's deadline. The context cancel
		// should propagate through net/http and cause Do() to return.
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := Contribute(ctx, "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{Endpoint: srv.URL})
	if err == nil {
		t.Error("expected timeout error; got nil")
	}
	if !errors.Is(err, ErrNetwork) {
		t.Errorf("got %v; want ErrNetwork (timeout/cancel should wrap as network)", err)
	}
}

func TestContribute_EnvVarOverridesDefaultEndpoint(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	t.Setenv("SMIRROR_TELEMETRY_ENDPOINT", srv.URL)

	// Note: NOT passing Endpoint in opts so the env var is the only
	// source.
	err := Contribute(context.Background(), "0.0.0-test",
		map[string]any{"event_kind": "first_seen"},
		ContributeOptions{})
	if err != nil {
		t.Errorf("Contribute = %v; want nil", err)
	}
	select {
	case <-hit:
	default:
		t.Error("env-var endpoint was not contacted")
	}
}
