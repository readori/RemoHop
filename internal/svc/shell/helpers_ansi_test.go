package shell

import "testing"

func TestPlainText(t *testing.T) {
	in := "\x1b[?25l\x1b[2J\x1b[mhello\x1b[1;5H-42\r\n\x1b]0;title\x07done\x1b[?25h"
	if got := plainText(in); got != "hello-42\ndone" {
		t.Fatalf("plainText = %q", got)
	}
}
