package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/webview"
)

// webview-selftest is for CI: it shows what the native web view of this
// system can do for the remote desktop (WebCodecs, codecs, decode time)
// and, with --browser, the same page in a browser for comparison. The
// program is a GUI program on Windows, so the report goes to --out.
func init() {
	cli.Register(&cli.Command{Name: "webview-selftest", Hidden: true, Summary: "check the native web view (CI)",
		Usage: "Usage: webview-selftest [--clip FILE.h264] [--out FILE] [--hold DURATION] [--browser PROGRAM [ARG...]]\n",
		Run: func(ctx context.Context, env *cli.Env, args []string) int {
			fs := flag.NewFlagSet("webview-selftest", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			clip := fs.String("clip", "", "")
			out := fs.String("out", "", "")
			hold := fs.Duration("hold", 0, "")
			browser := fs.Bool("browser", false, "")
			if err := fs.Parse(args); err != nil {
				env.Errorf("webview-selftest: %v", err)
				return cli.ExitUsage
			}
			var rep map[string]any
			var err error
			if *browser && fs.NArg() > 0 {
				rep, err = webview.SelfTestBrowser(*clip, 90*time.Second, func(url string) (func(), error) {
					cmd := exec.Command(fs.Arg(0), append(fs.Args()[1:], url)...)
					if err := cmd.Start(); err != nil {
						return nil, err
					}
					return func() { cmd.Process.Kill(); cmd.Wait() }, nil
				})
			} else {
				rep, err = webview.SelfTest(*clip, 90*time.Second, *hold)
			}
			if rep == nil {
				rep = map[string]any{}
			}
			if err != nil {
				rep["error"] = err.Error()
			}
			b, _ := json.MarshalIndent(rep, "", "  ")
			if *out != "" {
				os.WriteFile(*out, b, 0o644)
			}
			env.Stdout.Write(append(b, '\n'))
			if err != nil {
				return cli.ExitError
			}
			return cli.ExitOK
		}})
}
