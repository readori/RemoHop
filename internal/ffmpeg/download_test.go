package ffmpeg

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPinnedBuilds downloads every pinned build from every mirror (about
// 440 MB) and checks it as Install does: size, SHA-256, and the program
// inside. It proves the table in builds.go; run it after changing it:
//
//	REMOHOP_FFMPEG_DOWNLOAD_TEST=1 go test -run TestPinnedBuilds -v ./internal/ffmpeg
func TestPinnedBuilds(t *testing.T) {
	if os.Getenv("REMOHOP_FFMPEG_DOWNLOAD_TEST") != "1" {
		t.Skip("set REMOHOP_FFMPEG_DOWNLOAD_TEST=1 to download the pinned builds")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	c := httpClient(nil)
	for platform, b := range builds {
		for _, u := range b.URLs {
			t.Run(platform, func(t *testing.T) {
				dir := t.TempDir()
				archive, err := fetch(ctx, c, u, b, dir, func(string, int64, int64) {})
				if err != nil {
					t.Fatalf("%s: %v", u, err)
				}
				p, err := extract(archive, b, dir)
				if err != nil {
					t.Fatalf("%s: %v", u, err)
				}
				fi, err := os.Stat(p)
				if err != nil || fi.Size() != b.MemberSize {
					t.Fatalf("%s: extracted %v %v", u, fi, err)
				}
				t.Logf("%s: ffmpeg %s, %d bytes, SHA-256 %s: ok", u, b.Version, b.Size, b.SHA256)
			})
		}
	}
}
