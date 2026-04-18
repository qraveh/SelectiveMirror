//go:build windows

package task

import (
	"errors"
	"strings"
	"testing"
)

// fakeRunner captures schtasks.exe invocations and returns canned output.
type fakeRunner struct {
	calls   [][]string
	outputs [][]byte
	errs    []error
	idx     int
}

func (f *fakeRunner) run(name string, args ...string) ([]byte, error) {
	all := append([]string{name}, args...)
	f.calls = append(f.calls, all)
	var out []byte
	var err error
	if f.idx < len(f.outputs) {
		out = f.outputs[f.idx]
	}
	if f.idx < len(f.errs) {
		err = f.errs[f.idx]
	}
	f.idx++
	return out, err
}

// withFakes installs test-doubles for runner and currentUser. Returns a
// restore function that must be deferred.
func withFakes(t *testing.T, f *fakeRunner, principal string) func() {
	t.Helper()
	origRunner := runner
	origUser := currentUser
	runner = f.run
	currentUser = func() (string, error) { return principal, nil }
	return func() {
		runner = origRunner
		currentUser = origUser
	}
}

func TestIsInstalled_TaskPresent(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{[]byte("TaskName: \\SelectiveMirror\n")},
		errs:    []error{nil},
	}
	defer withFakes(t, f, `DOMAIN\user`)()

	ok, err := IsInstalled()
	if err != nil {
		t.Fatalf("IsInstalled: %v", err)
	}
	if !ok {
		t.Error("expected installed=true")
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 schtasks call, got %d", len(f.calls))
	}
	got := f.calls[0]
	want := []string{"schtasks.exe", "/Query", "/TN", TaskName, "/FO", "LIST"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Errorf("call = %v, want %v", got, want)
		}
	}
}

func TestIsInstalled_TaskAbsent(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{[]byte("ERROR: The system cannot find the file specified.\n")},
		errs:    []error{errors.New("exit 1")},
	}
	defer withFakes(t, f, `DOMAIN\user`)()

	ok, err := IsInstalled()
	if err != nil {
		t.Fatalf("IsInstalled: unexpected error %v", err)
	}
	if ok {
		t.Error("expected installed=false")
	}
}

func TestInstall_CreatesXMLAndCallsSchtasks(t *testing.T) {
	// Sequence: IsInstalled -> not installed (error output), then schtasks /Create
	f := &fakeRunner{
		outputs: [][]byte{
			[]byte("ERROR: The system cannot find the file specified.\n"), // IsInstalled
			[]byte("SUCCESS: The scheduled task has been created.\n"),     // /Create
		},
		errs: []error{errors.New("exit 1"), nil},
	}
	defer withFakes(t, f, `DOMAIN\alice`)()

	if err := Install(`C:\Users\alice\.selectivemirror\config.yaml`); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("expected 2 schtasks calls, got %d", len(f.calls))
	}
	if f.calls[1][0] != "schtasks.exe" {
		t.Errorf("second call binary = %q, want schtasks.exe", f.calls[1][0])
	}
	if f.calls[1][1] != "/Create" {
		t.Errorf("second call first arg = %q, want /Create", f.calls[1][1])
	}
	// Ensure /XML <tmpfile> and /F and /TN TaskName appear in the args.
	args := strings.Join(f.calls[1], " ")
	for _, tok := range []string{"/XML", "/F", "/TN", TaskName} {
		if !strings.Contains(args, tok) {
			t.Errorf("Install call missing %q: %s", tok, args)
		}
	}
}

func TestInstall_AlreadyInstalled(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{[]byte("TaskName: \\SelectiveMirror\n")},
		errs:    []error{nil},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	err := Install("x")
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("expected ErrAlreadyInstalled, got %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("expected only IsInstalled call; got %d", len(f.calls))
	}
}

func TestUninstall_NotInstalled(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{[]byte("ERROR: The system cannot find the file specified.\n")},
		errs:    []error{errors.New("exit 1")},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	err := Uninstall()
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestUninstall_Success(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{
			[]byte("TaskName: \\SelectiveMirror\n"), // IsInstalled
			[]byte("SUCCESS\n"),                     // /Delete
		},
		errs: []error{nil, nil},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(f.calls))
	}
	if f.calls[1][1] != "/Delete" || f.calls[1][4] != "/F" {
		t.Errorf("unexpected /Delete args: %v", f.calls[1])
	}
}

func TestStart_NotInstalled(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{[]byte("does not exist")},
		errs:    []error{errors.New("exit 1")},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	if err := Start(); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Start: want ErrNotInstalled, got %v", err)
	}
}

func TestStop_NotRunning_IsBenign(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{
			[]byte("TaskName: \\SelectiveMirror\n"),                      // IsInstalled
			[]byte("ERROR: The task is not currently running.\n"),        // /End
		},
		errs: []error{nil, errors.New("exit 1")},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	if err := Stop(); err != nil {
		t.Errorf("Stop when not running should be nil, got %v", err)
	}
}

func TestStop_RealError(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{
			[]byte("TaskName: \\SelectiveMirror\n"),
			[]byte("ERROR: Access denied.\n"),
		},
		errs: []error{nil, errors.New("exit 2")},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	err := Stop()
	if err == nil {
		t.Error("expected error from Stop when schtasks fails with unknown message")
	}
}

func TestQuery_ParsesLastRun(t *testing.T) {
	xmlResp := `<?xml version="1.0" encoding="UTF-16"?><Task/>`
	listResp := `
HostName:          MSI
TaskName:          \SelectiveMirror
Next Run Time:     4/19/2026 9:00:00 AM
Status:            Ready
Last Run Time:     4/18/2026 6:12:00 PM
Last Result:       0
`
	f := &fakeRunner{
		outputs: [][]byte{
			[]byte(xmlResp),
			[]byte(listResp),
		},
		errs: []error{nil, nil},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	s, err := Query()
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !s.Installed {
		t.Error("expected Installed=true")
	}
	if s.Running {
		t.Error("Status=Ready should map to Running=false")
	}
	if !strings.Contains(s.LastRunTime, "6:12:00 PM") {
		t.Errorf("LastRunTime = %q, want something with 6:12:00 PM", s.LastRunTime)
	}
	if s.LastRunResult != "0" {
		t.Errorf("LastRunResult = %q, want 0", s.LastRunResult)
	}
}

func TestQuery_RunningState(t *testing.T) {
	listResp := "TaskName: \\SelectiveMirror\nStatus: Running\nLast Run Time: N/A\nLast Result: 267009\nNext Run Time: N/A\n"
	f := &fakeRunner{
		outputs: [][]byte{
			[]byte(`<?xml version="1.0"?><Task/>`),
			[]byte(listResp),
		},
		errs: []error{nil, nil},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	s, err := Query()
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !s.Running {
		t.Error("Status=Running should map to Running=true")
	}
	if s.LastRunTime != "" {
		t.Errorf("N/A LastRunTime should remain empty, got %q", s.LastRunTime)
	}
}

func TestQuery_NotInstalled(t *testing.T) {
	f := &fakeRunner{
		outputs: [][]byte{[]byte("ERROR: The system cannot find the file specified.")},
		errs:    []error{errors.New("exit 1")},
	}
	defer withFakes(t, f, `DOMAIN\u`)()

	s, err := Query()
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if s.Installed {
		t.Error("expected Installed=false")
	}
}

func TestEncodeUTF16LEWithBOM(t *testing.T) {
	out := encodeUTF16LEWithBOM([]byte("A"))
	if len(out) != 4 {
		t.Fatalf("UTF-16LE of 'A' should be 4 bytes (BOM+1 rune), got %d", len(out))
	}
	if out[0] != 0xFF || out[1] != 0xFE {
		t.Errorf("BOM = %x %x, want FF FE", out[0], out[1])
	}
	if out[2] != 'A' || out[3] != 0 {
		t.Errorf("encoded 'A' = %x %x, want 41 00", out[2], out[3])
	}
}

func TestEncodeUTF16LEWithBOM_NonASCII(t *testing.T) {
	// "日" = U+65E5 — should encode to E5 65 in UTF-16LE.
	out := encodeUTF16LEWithBOM([]byte("日"))
	if len(out) != 4 { // BOM + one 16-bit code unit
		t.Fatalf("len = %d, want 4", len(out))
	}
	if out[2] != 0xE5 || out[3] != 0x65 {
		t.Errorf("encoded 日 = %x %x, want E5 65", out[2], out[3])
	}
}
