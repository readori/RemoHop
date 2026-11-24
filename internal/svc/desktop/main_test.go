package desktop

import (
	"io"
	"log"
	"os"
	"testing"

	"github.com/jezek/xgb"
)

func TestMain(m *testing.M) {
	// xgb complains about the missing ~/.Xauthority on every connection to
	// the test's private Xvfb.
	xgb.Logger = log.New(io.Discard, "", 0)
	os.Exit(m.Run())
}
