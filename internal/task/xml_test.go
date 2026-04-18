package task

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestRenderXML_BasicFields(t *testing.T) {
	def := XMLDefinition{
		Author:           "TestAuthor",
		Description:      "Test desc",
		RegistrationTime: time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC),
		UserPrincipal:    `MSI\raveh`,
		Command:          `C:\Program Files\SelectiveMirror\smirror.exe`,
		Arguments:        `start --config "C:\Users\raveh\.selectivemirror\config.yaml"`,
	}
	out, err := def.RenderXML()
	if err != nil {
		t.Fatalf("RenderXML: %v", err)
	}
	s := string(out)

	wantSubstrings := []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"`,
		`<Date>2026-04-18T12:00:00</Date>`,
		`<Author>TestAuthor</Author>`,
		`<Description>Test desc</Description>`,
		`<URI>\SelectiveMirror</URI>`,
		`<LogonTrigger>`,
		`<UserId>MSI\raveh</UserId>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<RunLevel>LeastPrivilege</RunLevel>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<RestartOnFailure>`,
		`<Interval>PT1M</Interval>`,
		`<Count>3</Count>`,
		`<Command>C:\Program Files\SelectiveMirror\smirror.exe</Command>`,
		`<Arguments>start --config &#34;C:\Users\raveh\.selectivemirror\config.yaml&#34;</Arguments>`,
		`<WorkingDirectory>C:\Program Files\SelectiveMirror</WorkingDirectory>`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(s, want) {
			t.Errorf("rendered XML missing %q\n---\n%s", want, s)
		}
	}
}

func TestRenderXML_ValidatesRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		def  XMLDefinition
		want string
	}{
		{"missing UserPrincipal", XMLDefinition{Command: "x"}, "UserPrincipal is required"},
		{"missing Command", XMLDefinition{UserPrincipal: "u"}, "Command is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.def.RenderXML()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing %q", err, tc.want)
			}
		})
	}
}

func TestRenderXML_DefaultsApplied(t *testing.T) {
	def := XMLDefinition{
		UserPrincipal: "u",
		Command:       `C:\x\smirror.exe`,
	}
	out, err := def.RenderXML()
	if err != nil {
		t.Fatalf("RenderXML: %v", err)
	}
	s := string(out)
	// Default author
	if !strings.Contains(s, "<Author>SelectiveMirror</Author>") {
		t.Errorf("default author not applied: %s", s)
	}
	// Default description starts with "SelectiveMirror file sync"
	if !strings.Contains(s, "SelectiveMirror file sync") {
		t.Errorf("default description not applied: %s", s)
	}
	// Default working directory = parent of Command
	if !strings.Contains(s, `<WorkingDirectory>C:\x</WorkingDirectory>`) {
		t.Errorf("default working directory not applied: %s", s)
	}
	// Default registration time uses now() — should at least parse as a valid date.
	// Extract the <Date> value and try to parse it.
	start := strings.Index(s, "<Date>") + len("<Date>")
	end := strings.Index(s, "</Date>")
	dateStr := s[start:end]
	if _, err := time.Parse("2006-01-02T15:04:05", dateStr); err != nil {
		t.Errorf("default date %q is not a valid timestamp: %v", dateStr, err)
	}
}

func TestRenderXML_WellFormed(t *testing.T) {
	def := XMLDefinition{
		UserPrincipal: `DOMAIN\user`,
		Command:       `C:\x\y.exe`,
		Arguments:     `--config "C:\Users\user\my dir\config.yaml"`,
	}
	out, err := def.RenderXML()
	if err != nil {
		t.Fatalf("RenderXML: %v", err)
	}
	// Strip the UTF-16 declaration line; xml.Unmarshal wants a <Task> root.
	body := string(out)
	if i := strings.Index(body, "<Task "); i >= 0 {
		body = body[i:]
	}
	var generic struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal([]byte(body), &generic); err != nil {
		t.Errorf("rendered XML does not parse: %v", err)
	}
	if generic.XMLName.Local != "Task" {
		t.Errorf("root element = %q, want Task", generic.XMLName.Local)
	}
}

func TestRenderXML_SpecialCharsEscaped(t *testing.T) {
	// Paths and args may contain &, <, >, " — all must be XML-escaped.
	def := XMLDefinition{
		UserPrincipal: `DOMAIN\user`,
		Command:       `C:\fun & games\smirror.exe`,
		Arguments:     `--config "<weird>&path"`,
	}
	out, err := def.RenderXML()
	if err != nil {
		t.Fatalf("RenderXML: %v", err)
	}
	s := string(out)
	// Raw & and < should not appear inside element values (outside the CDATA
	// we never use). Check that the encoded forms appear.
	if !strings.Contains(s, "&amp;") {
		t.Errorf("expected &amp; in output: %s", s)
	}
	if !strings.Contains(s, "&lt;weird&gt;") {
		t.Errorf("expected escaped <weird>: %s", s)
	}
	// The actual raw substring "fun & games" (with a literal ampersand) must
	// not appear; it should be "fun &amp; games".
	if strings.Contains(s, "fun & games") {
		t.Errorf("unescaped ampersand in path: %s", s)
	}
}

func TestFilepathDir(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`C:\foo\bar.exe`, `C:\foo`},
		{`/home/user/smirror`, `/home/user`},
		{`bar.exe`, `.`},
		{`C:\bar.exe`, `C:`}, // no leading slash → up to just before separator
		{``, `.`},
	}
	for _, tc := range cases {
		if got := filepathDir(tc.in); got != tc.want {
			t.Errorf("filepathDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
