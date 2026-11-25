package webview

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// selfTestPage checks what the remote-desktop viewer needs from the web
// view (WebCodecs and which codecs it decodes) and measures how long a
// decode takes: the clip is fed at 30 frames a second, as a host would
// send it, and the time from decode() to the decoded frame is recorded.
const selfTestPage = `<!doctype html><html><head><meta charset="utf-8"><title>RemoHop web view self-test</title>
<style>body{font:15px system-ui,sans-serif;margin:24px;background:#f7f8ff;color:#151b2c}h1{color:#2546D8;font-size:20px}pre{white-space:pre-wrap}</style>
</head><body><h1>RemoHop web view self-test</h1><canvas id=c width=480 height=270></canvas><pre id=o>running…</pre><script>
const out={ua:navigator.userAgent,videoDecoder:typeof VideoDecoder,videoEncoder:typeof VideoEncoder,secure:window.isSecureContext};
const show=()=>{document.getElementById('o').textContent=JSON.stringify(out,null,1)};
const report=()=>{show();fetch('report',{method:'POST',body:JSON.stringify(out)})};
function split(b){const a=[];let s=-1;for(let i=0;i+4<b.length;i++){if(b[i]===0&&b[i+1]===0&&b[i+2]===1){const t=b[i+3]&31,p=(i>0&&b[i-1]===0)?i-1:i;if(t===9){if(s>=0)a.push(b.subarray(s,p));s=p}i+=2}}if(s>=0)a.push(b.subarray(s));return a}
(async()=>{try{
 if(typeof VideoDecoder==='undefined'){report();return}
 for(const [k,c] of [['h264','avc1.42001f'],['h264_1080','avc1.42002a'],['av1','av01.0.08M.08']])
  for(const hw of ['prefer-hardware','prefer-software']){try{out[k+'.'+hw]=(await VideoDecoder.isConfigSupported({codec:c,codedWidth:1920,codedHeight:1080,optimizeForLatency:true,hardwareAcceleration:hw})).supported}catch(e){out[k+'.'+hw]='error: '+e}}
 const r=await fetch('clip.h264');if(!r.ok){out.clip='none';report();return}
 const aus=split(new Uint8Array(await r.arrayBuffer()));out.frames=aus.length;
 const ctx=document.getElementById('c').getContext('2d');
 // Each pass feeds the clip at 30 fps, as a host sends it, and records the
 // time from decode() to the decoded frame.
 async function pass(hw){
  const sent=new Map(),lat=[];let errs=0,err='';
  const dec=new VideoDecoder({output:f=>{const t=sent.get(f.timestamp);if(t!==undefined)lat.push(performance.now()-t);ctx.drawImage(f,0,0,480,270);f.close()},error:e=>{errs++;err=String(e)}});
  dec.configure({codec:'avc1.42002a',codedWidth:1920,codedHeight:1080,optimizeForLatency:true,hardwareAcceleration:hw});
  for(let i=0;i<aus.length&&dec.state==='configured';i++){const ts=i*33333;sent.set(ts,performance.now());dec.decode(new EncodedVideoChunk({type:i===0?'key':'delta',timestamp:ts,data:aus[i]}));await new Promise(r=>setTimeout(r,33))}
  const beforeFlush=lat.length;
  if(dec.state==='configured')await dec.flush().catch(e=>err=String(e));
  lat.sort((a,b)=>a-b);
  const o={decoded:lat.length,beforeFlush,errors:errs};
  if(err)o.error=err;
  if(lat.length)o.latencyMs={p50:+lat[lat.length>>1].toFixed(1),p95:+lat[Math.floor(lat.length*.95)].toFixed(1),max:+lat[lat.length-1].toFixed(1)};
  try{dec.close()}catch(e){}
  return o;
 }
 out.decode={'no-preference':await pass('no-preference'),'prefer-software':await pass('prefer-software'),'prefer-hardware':await pass('prefer-hardware')};
}catch(e){out.exception=String(e)}report()})();
</script></body></html>`

// SelfTest opens a native window with a page that probes WebCodecs and
// decodes clip (an H.264 Annex B stream with access unit delimiters; ""
// skips the decode), and returns what the page reported. The window stays
// open for hold after the report, so that a test can take a screenshot.
// Like Run, it must be called from the main goroutine.
func SelfTest(clip string, timeout, hold time.Duration) (map[string]any, error) {
	return selfTest(clip, timeout, hold, nil)
}

// SelfTestBrowser runs the same page in a browser instead: start is called
// with the page's address and must open it (and may return a process to
// end afterwards). It is the comparison for SelfTest.
func SelfTestBrowser(clip string, timeout time.Duration, start func(url string) (stop func(), err error)) (map[string]any, error) {
	return selfTest(clip, timeout, 0, start)
}

func selfTest(clip string, timeout, hold time.Duration, browser func(string) (func(), error)) (map[string]any, error) {
	var info Info
	if browser == nil {
		var err error
		if info, err = Probe(); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	reports := make(chan []byte, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, selfTestPage)
	})
	mux.HandleFunc("GET /clip.h264", func(w http.ResponseWriter, r *http.Request) {
		if clip == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, clip)
	})
	mux.HandleFunc("POST /report", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		select {
		case reports <- b:
		default:
		}
	})
	go http.Serve(ln, mux)

	var out map[string]any
	var runErr error
	pageURL := "http://" + ln.Addr().String() + "/"
	if browser != nil {
		stop, err := browser(pageURL)
		if err != nil {
			return nil, err
		}
		if stop != nil {
			defer stop()
		}
		select {
		case b := <-reports:
			err = json.Unmarshal(b, &out)
			return out, err
		case <-time.After(timeout):
			return nil, errors.New("the page did not report in time")
		}
	}
	err = Run(Config{AppID: "remohop", DataDir: selfTestDataDir()}, func(app App) {
		start := time.Now()
		_, werr := app.NewWindow(Options{Title: "RemoHop web view self-test", URL: pageURL, Width: 640, Height: 720})
		if werr != nil {
			runErr = werr
			app.Quit()
			return
		}
		go func() {
			select {
			case b := <-reports:
				if err := json.Unmarshal(b, &out); err != nil {
					runErr = err
				}
				if out != nil {
					out["engine"], out["engineVersion"] = info.Engine, info.Version
					out["reportAfterMs"] = time.Since(start).Milliseconds()
				}
				time.Sleep(hold)
			case <-time.After(timeout):
				runErr = errors.New("the page did not report in time")
			}
			app.Quit()
		}()
	})
	if err != nil {
		return nil, err
	}
	return out, runErr
}

func selfTestDataDir() string {
	d, err := os.MkdirTemp("", "remohop-webview-selftest-")
	if err != nil {
		return ""
	}
	return d
}
