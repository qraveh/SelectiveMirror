package telemetry

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// OSDetail returns the Windows version string, e.g. "Windows 11 23H2".
func OSDetail() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer k.Close()

	productName, _, _ := k.GetStringValue("ProductName")
	displayVersion, _, _ := k.GetStringValue("DisplayVersion") // e.g. "23H2"
	buildNumber, _, _ := k.GetStringValue("CurrentBuildNumber")

	var parts []string
	if productName != "" {
		parts = append(parts, productName)
	} else {
		parts = append(parts, "Windows")
	}
	if displayVersion != "" {
		parts = append(parts, displayVersion)
	}
	if buildNumber != "" {
		parts = append(parts, fmt.Sprintf("(Build %s)", buildNumber))
	}
	return strings.Join(parts, " ")
}
