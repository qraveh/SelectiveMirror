package notify

// Allow tests using httptest.NewServer (bound to 127.0.0.1) to reach the
// webhook sender. The flag is unexported and lives only in webhook.go; this
// file is *_test.go and is only compiled into the test binary, so no
// production code path can disable the SSRF defense. SEC-C4 / SEC-H7.
func init() {
	allowLoopbackWebhooks = true
}
