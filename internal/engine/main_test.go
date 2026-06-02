package engine

import (
	"os"
	"testing"
)

// TestMain runs the package's tests in a throwaway working directory so the
// engine's .DAT / .session autosaves never litter the source tree.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "toneloc-test-*")
	if err == nil {
		old, _ := os.Getwd()
		os.Chdir(dir)
		code := m.Run()
		os.Chdir(old)
		os.RemoveAll(dir)
		os.Exit(code)
	}
	os.Exit(m.Run())
}
