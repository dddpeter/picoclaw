package feishu

import "testing"

func TestSanitizeFeishuMarkdownImages(t *testing.T) {
	cases := []struct{ in, want string }{
		{"![QR](lark_qr.png)", "[QR](lark_qr.png)"},
		{"![img](img_v2_abc123)", "![img](img_v2_abc123)"}, // valid key kept
		{"![img](img_v3_xyz)", "![img](img_v3_xyz)"},       // valid v3 kept
		{"no images here", "no images here"},
		{"a ![x](/tmp/foo.png) b ![y](img_v2_ok) c", "a [x](/tmp/foo.png) b ![y](img_v2_ok) c"},
	}
	for _, c := range cases {
		if got := sanitizeFeishuMarkdownImages(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
