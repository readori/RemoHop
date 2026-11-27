//go:build unix

package xfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/readori/RemoHop/internal/grant"
)

type rulesGuard struct{ r *grant.Rules }

func (g rulesGuard) Name() string { return "test" }
func (g rulesGuard) Command(context.Context, string, string, bool, []string) error {
	return &grant.Refusal{Grant: "test", What: "commands"}
}
func (g rulesGuard) Terminal(context.Context, string) error {
	return &grant.Refusal{Grant: "test", What: "terminals"}
}
func (g rulesGuard) WorkDir(context.Context, string, string) error {
	return &grant.Refusal{Grant: "test", What: "commands"}
}
func (g rulesGuard) File(_ context.Context, _ string, a grant.Access, p string) error {
	if err := g.r.CheckPath(p, a); err != nil {
		return &grant.Refusal{Grant: "test", What: a.String(), Why: err.Error()}
	}
	return nil
}
func (g rulesGuard) Log(string, ...string)                          {}
func (g rulesGuard) Record(string, string, int, int) grant.Recorder { return nil }
func (g rulesGuard) FileScope() (*grant.Scope, error)               { return g.r.Scope(), nil }

// `remohop cp` resumes an interrupted upload for a session on a grant,
// through the partial file the host names (audit finding 3).
func TestResumableUploadUnderGrant(t *testing.T) {
	const size = 32 << 20
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	os.Mkdir(outDir, 0o755)
	r, err := grant.Parse([]string{"files:write:" + outDir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hostGuard = rulesGuard{r}
	defer func() { hostGuard = nil }()
	data := make([]byte, size)
	rand.Read(data)
	srcPath := filepath.Join(dir, "src.bin")
	os.WriteFile(srcPath, data, 0o644)
	src, dst := Path{Path: srcPath}, Path{Remote: true, Path: outDir}
	interruptedCopy(t, src, dst, 12<<20)
	waitStablePartial(t, outDir, "src.bin")
	p := copyWith(t, src, dst, CopyOptions{})
	done, resumed := p.BytesDone.Load(), p.BytesResumed.Load()
	if resumed < 8<<20 || done+resumed != size {
		t.Fatalf("did not resume: %d transferred + %d resumed of %d", done, resumed, size)
	}
	if b, _ := os.ReadFile(filepath.Join(outDir, "src.bin")); !bytes.Equal(b, data) {
		t.Fatal("resumed file differs")
	}
	if ps := partials(t, outDir, "src.bin"); len(ps) != 0 {
		t.Fatalf("partials left: %v", ps)
	}
	// A different credential (a reissued grant) starts over.
	os.Remove(filepath.Join(outDir, "src.bin"))
	interruptedCopy(t, src, dst, 12<<20)
	waitStablePartial(t, outDir, "src.bin")
	r2, _ := grant.Parse([]string{"files:write:" + outDir}, nil)
	hostGuard = rulesGuard{r2}
	p = copyWith(t, src, dst, CopyOptions{})
	if p.BytesResumed.Load() != 0 || p.BytesDone.Load() != size {
		t.Fatalf("resumed under another credential: %d + %d", p.BytesDone.Load(), p.BytesResumed.Load())
	}
	if b, _ := os.ReadFile(filepath.Join(outDir, "src.bin")); !bytes.Equal(b, data) {
		t.Fatal("file differs")
	}
	if ps := partials(t, outDir, "src.bin"); len(ps) != 0 {
		t.Fatalf("partials left: %v", ps)
	}
}
