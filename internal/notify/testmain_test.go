package notify

// Allow tests using httptest.NewServer (bound to 127.0.0.1) to reach the
// webhook sender. Production code leaves this false; see SEC-C4.
func init() {
	AllowLoopbackWebhooks = true
}
