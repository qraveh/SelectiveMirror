package task

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// XMLDefinition is the data used to render a Windows Task Scheduler XML task
// file. Windows Task Scheduler consumes UTF-16LE with BOM XML; the renderer
// handles encoding.
type XMLDefinition struct {
	// Author shows in the task properties. Defaults to "SelectiveMirror" if empty.
	Author string
	// Description shows in the task properties.
	Description string
	// RegistrationTime becomes the <Date> under RegistrationInfo. If zero,
	// time.Now() is used at render time.
	RegistrationTime time.Time
	// UserPrincipal is the "DOMAIN\user" form of the account that owns and
	// runs the task (usually the installing user's principal). Must be set.
	UserPrincipal string
	// Command is the absolute path to the smirror executable. Must be set.
	Command string
	// Arguments is the full command-line argument string for the task
	// (already quoted as needed). Typically: `start --config "<path>"`.
	Arguments string
	// WorkingDirectory is passed to <Exec><WorkingDirectory>. Optional;
	// defaults to the directory of Command.
	WorkingDirectory string
}

// RenderXML produces the Windows Task Scheduler XML definition for a logon
// task. Output is UTF-8 (byte slice); the caller must transcode to UTF-16LE
// with BOM before handing the file to schtasks.exe.
func (d XMLDefinition) RenderXML() ([]byte, error) {
	if d.UserPrincipal == "" {
		return nil, fmt.Errorf("task XML: UserPrincipal is required")
	}
	if d.Command == "" {
		return nil, fmt.Errorf("task XML: Command is required")
	}

	author := d.Author
	if author == "" {
		author = "SelectiveMirror"
	}
	description := d.Description
	if description == "" {
		description = "SelectiveMirror file sync — runs at user logon."
	}
	regTime := d.RegistrationTime
	if regTime.IsZero() {
		regTime = time.Now().UTC()
	}

	// Build the task using encoding/xml Marshal to guarantee well-formed
	// output and correct escaping of filenames / paths with special chars.
	type logonTrigger struct {
		XMLName xml.Name `xml:"LogonTrigger"`
		Enabled string   `xml:"Enabled"`
		UserID  string   `xml:"UserId"`
	}
	type principal struct {
		XMLName   xml.Name `xml:"Principal"`
		ID        string   `xml:"id,attr"`
		UserID    string   `xml:"UserId"`
		LogonType string   `xml:"LogonType"`
		RunLevel  string   `xml:"RunLevel"`
	}
	type idleSettings struct {
		XMLName       xml.Name `xml:"IdleSettings"`
		StopOnIdleEnd string   `xml:"StopOnIdleEnd"`
		RestartOnIdle string   `xml:"RestartOnIdle"`
	}
	type restartOnFailure struct {
		XMLName  xml.Name `xml:"RestartOnFailure"`
		Interval string   `xml:"Interval"`
		Count    string   `xml:"Count"`
	}
	// Task Scheduler schema 1.2 (Windows 7+). The element order matters —
	// Task Scheduler validates against its XSD and rejects out-of-order or
	// unknown children. Elements introduced in schema 1.3 or later
	// (DisallowStartOnRemoteAppSession, UseUnifiedSchedulingEngine) are
	// intentionally omitted to keep the generated XML compatible with all
	// supported Windows versions.
	type settings struct {
		XMLName                    xml.Name `xml:"Settings"`
		MultipleInstancesPolicy    string   `xml:"MultipleInstancesPolicy"`
		DisallowStartIfOnBatteries string   `xml:"DisallowStartIfOnBatteries"`
		StopIfGoingOnBatteries     string   `xml:"StopIfGoingOnBatteries"`
		AllowHardTerminate         string   `xml:"AllowHardTerminate"`
		StartWhenAvailable         string   `xml:"StartWhenAvailable"`
		RunOnlyIfNetworkAvailable  string   `xml:"RunOnlyIfNetworkAvailable"`
		IdleSettings               idleSettings
		AllowStartOnDemand         string           `xml:"AllowStartOnDemand"`
		Enabled                    string           `xml:"Enabled"`
		Hidden                     string           `xml:"Hidden"`
		RunOnlyIfIdle              string           `xml:"RunOnlyIfIdle"`
		WakeToRun                  string           `xml:"WakeToRun"`
		ExecutionTimeLimit         string           `xml:"ExecutionTimeLimit"`
		Priority                   string           `xml:"Priority"`
		RestartOnFailure           restartOnFailure `xml:"RestartOnFailure"`
	}
	type execAction struct {
		XMLName          xml.Name `xml:"Exec"`
		Command          string   `xml:"Command"`
		Arguments        string   `xml:"Arguments,omitempty"`
		WorkingDirectory string   `xml:"WorkingDirectory,omitempty"`
	}
	type actions struct {
		XMLName xml.Name   `xml:"Actions"`
		Context string     `xml:"Context,attr"`
		Exec    execAction `xml:"Exec"`
	}
	type regInfo struct {
		XMLName     xml.Name `xml:"RegistrationInfo"`
		Date        string   `xml:"Date"`
		Author      string   `xml:"Author"`
		Description string   `xml:"Description"`
		URI         string   `xml:"URI"`
	}
	type task struct {
		XMLName          xml.Name `xml:"Task"`
		Version          string   `xml:"version,attr"`
		Xmlns            string   `xml:"xmlns,attr"`
		RegistrationInfo regInfo
		Triggers         struct {
			LogonTrigger logonTrigger
		} `xml:"Triggers"`
		Principals struct {
			Principal principal
		} `xml:"Principals"`
		Settings settings
		Actions  actions
	}

	workingDir := d.WorkingDirectory
	if workingDir == "" {
		// Default working directory is the parent folder of the command.
		workingDir = filepathDir(d.Command)
	}

	t := task{
		Version: "1.2",
		Xmlns:   "http://schemas.microsoft.com/windows/2004/02/mit/task",
		RegistrationInfo: regInfo{
			Date:        regTime.Format("2006-01-02T15:04:05"),
			Author:      author,
			Description: description,
			URI:         "\\" + TaskName,
		},
	}
	t.Triggers.LogonTrigger = logonTrigger{
		Enabled: "true",
		UserID:  d.UserPrincipal,
	}
	t.Principals.Principal = principal{
		ID:        "Author",
		UserID:    d.UserPrincipal,
		LogonType: "InteractiveToken",
		RunLevel:  "LeastPrivilege",
	}
	t.Settings = settings{
		MultipleInstancesPolicy:    "IgnoreNew",
		DisallowStartIfOnBatteries: "false",
		StopIfGoingOnBatteries:     "false",
		AllowHardTerminate:         "true",
		StartWhenAvailable:         "true",
		RunOnlyIfNetworkAvailable:  "false",
		IdleSettings:               idleSettings{StopOnIdleEnd: "false", RestartOnIdle: "false"},
		AllowStartOnDemand:         "true",
		Enabled:                    "true",
		Hidden:                     "false",
		RunOnlyIfIdle:              "false",
		WakeToRun:                  "false",
		ExecutionTimeLimit:         "PT0S",
		Priority:                   "7",
		RestartOnFailure:           restartOnFailure{Interval: "PT1M", Count: "3"},
	}
	t.Actions = actions{
		Context: "Author",
		Exec: execAction{
			Command:          d.Command,
			Arguments:        d.Arguments,
			WorkingDirectory: workingDir,
		},
	}

	body, err := xml.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal task XML: %w", err)
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>`)
	b.WriteByte('\n')
	b.Write(body)
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

// filepathDir returns the parent directory portion of path, using the last
// backslash or forward slash as the separator. Kept self-contained to avoid
// platform-specific filepath semantics inside the XML renderer (the value is
// literal text inside the XML, not OS-interpreted).
func filepathDir(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		if i == 0 {
			return path[:1]
		}
		return path[:i]
	}
	return "."
}
