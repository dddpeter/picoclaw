package fstools

import "testing"

// m-2: normalizeSearchRoot must preserve POSIX absolute paths — the previous
// two-sided strings.Trim re-rooted "/tmp" into "<workspace>/tmp".
func TestNormalizeSearchRoot(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{".", ""},
		{"./", ""},
		{"pkg", "pkg"},
		{"pkg/", "pkg"},
		{"./pkg/sub/", "pkg/sub"},
		{"/tmp", "/tmp"},
		{"/etc/nginx/", "/etc/nginx"},
		{"D:/code/picoclaw", "D:/code/picoclaw"},
		{"D:/code/picoclaw/", "D:/code/picoclaw"},
		{`D:\code\picoclaw\pkg`, "D:/code/picoclaw/pkg"},
	}
	for _, tc := range cases {
		if got := normalizeSearchRoot(tc.in); got != tc.want {
			t.Errorf("normalizeSearchRoot(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
