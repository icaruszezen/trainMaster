package dataversion

import "testing"

func TestOriginalPathListSafe(t *testing.T) {
	n := newPathNamer()
	for _, c := range []struct{ rel, want string }{
		{"a/b.jpg", "images/ds/a/b.jpg"},
		{"lot.../x.jpg", "images/ds/lot___/x.jpg"},
		{"x\ny/a\rb.jpg", "images/ds/x_y/a_b.jpg"},
		{"p q/c.v1.png", "images/ds/p_q/c.v1.png"},
		{"a/b.jpg", "images/ds/a/b~2.jpg"},
	} {
		if got := n.originalPath("ds", c.rel); got != c.want {
			t.Errorf("originalPath(%q) = %q, want %q", c.rel, got, c.want)
		}
	}
	if got := n.originalPath("line.", "a.jpg"); got != "images/line_/a.jpg" {
		t.Errorf("dataset folder line. = %q", got)
	}
}
