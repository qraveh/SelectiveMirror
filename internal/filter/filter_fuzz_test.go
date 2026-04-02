package filter

import (
	"testing"
)

// FuzzFilterIsExcluded tests that IsExcluded never panics on arbitrary paths.
func FuzzFilterIsExcluded(f *testing.F) {
	// Seed corpus with normal and edge-case paths
	f.Add("normal.txt")
	f.Add("path/to/deep/file.go")
	f.Add(".git/config")
	f.Add("file with spaces.doc")
	f.Add("日本語ファイル.txt")
	f.Add("CON.txt")         // Windows reserved name
	f.Add("file[bracket].txt")
	f.Add("")
	f.Add("../escape/attempt")
	f.Add(string(make([]byte, 300))) // long path

	fe, err := New([]string{"*.tmp", "build/", "!important.tmp", ".git/", "*.log"}, "")
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, path string) {
		// Must not panic on any input
		_ = fe.IsExcluded(path)
	})
}
