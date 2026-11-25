package desktop

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/x11disp"
)

// A host without an X display answers in one sentence, with what it tried
// (shortened) after "Details: ", which the web console shows on request.
func TestNoDisplayError(t *testing.T) {
	notes := []string{"DISPLAY is not set"}
	for i := range 27 {
		notes = append(notes, fmt.Sprintf("X display :%d: connection refused", i))
	}
	err := noDisplayError(&x11disp.Error{Notes: notes})
	msg := err.Error()
	short, details, ok := strings.Cut(msg, " Details: ")
	if !ok || !strings.Contains(short, "no graphical desktop session") || strings.Contains(short, "X display :") {
		t.Fatalf("message %q", msg)
	}
	if !strings.HasSuffix(details, "and 24 more") || strings.Count(details, ";") != 4 {
		t.Errorf("details %q", details)
	}
	if _, ok := errors.AsType[*x11disp.Error](err); !ok {
		t.Error("the cause is not kept")
	}
}
