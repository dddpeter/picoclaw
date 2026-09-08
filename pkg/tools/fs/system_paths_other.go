//go:build !windows

package fstools

// longPathForm is a Windows-only concept (8.3 short names); other platforms
// have no equivalent path aliasing.
func longPathForm(p string) (string, bool) {
	return "", false
}
