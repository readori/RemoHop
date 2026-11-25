package webview

// Windows: WebView2 through its COM interface (github.com/jchv/go-webview2's
// edge package, which embeds Microsoft's BSD-licensed WebView2Loader and
// loads it from memory), in plain Win32 windows; the tray icon is
// Shell_NotifyIcon. All system calls, no cgo.

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/dpiaware"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	ole32   = windows.NewLazySystemDLL("ole32.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pShowWindow               = user32.NewProc("ShowWindow")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pIsIconic                 = user32.NewProc("IsIconic")
	pSetWindowTextW           = user32.NewProc("SetWindowTextW")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pLoadImageW               = user32.NewProc("LoadImageW")
	pLoadIconW                = user32.NewProc("LoadIconW")
	pGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	pGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	pCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	pAppendMenuW              = user32.NewProc("AppendMenuW")
	pTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	pDestroyMenu              = user32.NewProc("DestroyMenu")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	pShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	pCoInitializeEx           = ole32.NewProc("CoInitializeEx")
	pGetModuleHandleW         = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	pFlashWindow              = user32.NewProc("FlashWindow")
	pBringWindowToTop         = user32.NewProc("BringWindowToTop")
	pAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	pSystemParametersInfoW    = user32.NewProc("SystemParametersInfoW")
)

const (
	wmDestroy       = 0x0002
	wmMove          = 0x0003
	wmSize          = 0x0005
	wmClose         = 0x0010
	wmNull          = 0x0000
	wmGetMinMaxInfo = 0x0024
	wmLButtonUp     = 0x0202
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B
	wmApp           = 0x8000
	wmDispatch      = wmApp + 1
	wmTray          = wmApp + 2

	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsBorder           = 0x00800000
	wsExTopmost        = 0x00000008
	wsExToolWindow     = 0x00000080
	wsExNoActivate     = 0x08000000
	cwUseDefault       = 0x80000000

	swHide        = 0
	swShow        = 5
	swRestore     = 9
	swShowNoActiv = 4

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10

	mfString    = 0x0
	mfGrayed    = 0x1
	mfSeparator = 0x800
	tpmReturn   = 0x0100
	tpmRight    = 0x0002

	smCxScreen = 0
)

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type point struct{ X, Y int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type minMaxInfo struct {
	Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize point
}

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     windows.Handle
}

func probe() (Info, error) {
	v, err := webviewloader.GetInstalledVersion()
	if err != nil {
		return Info{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if v == "" {
		return Info{}, fmt.Errorf("%w: the Microsoft Edge WebView2 runtime is not installed", ErrUnavailable)
	}
	return Info{Engine: "WebView2", Version: v}, nil
}

type winApp struct {
	cfg      Config
	instance windows.Handle
	icon     uintptr
	helper   uintptr // hidden window: dispatch and tray messages
	dpi      int

	qmu   sync.Mutex
	queue []func()

	windows map[uintptr]*winWindow
	tray    *Tray
	trayOn  bool
	trayTip string
	taskbar uint32 // "TaskbarCreated": Explorer restarted, add the icon again
}

type winWindow struct {
	app    *winApp
	hwnd   uintptr
	opts   Options
	web    *edge.Chromium
	closed bool
}

var (
	theApp     *winApp
	classOnce  sync.Once
	className  = windows.StringToUTF16Ptr("RemoHopWebView")
	helperName = windows.StringToUTF16Ptr("RemoHopHelper")
)

func run(cfg Config, setup func(App)) error {
	if _, err := probe(); err != nil {
		return err
	}
	dpiaware.Enable()
	pCoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED; WebView2 needs an STA
	inst, _, _ := pGetModuleHandleW.Call(0)
	a := &winApp{cfg: cfg, instance: windows.Handle(inst), windows: map[uintptr]*winWindow{}, dpi: 96}
	if d, _, _ := pGetDpiForSystem.Call(); d != 0 {
		a.dpi = int(d)
	}
	// The program's icon (resource 1, see packaging/quick/gen), else the
	// standard application icon.
	a.icon, _, _ = pLoadImageW.Call(inst, 1, 1 /* IMAGE_ICON */, 0, 0, 0x40|0x8000 /* LR_DEFAULTSIZE|LR_SHARED */)
	if a.icon == 0 {
		a.icon, _, _ = pLoadIconW.Call(0, 32512 /* IDI_APPLICATION */)
	}
	theApp = a
	var regErr error
	classOnce.Do(func() {
		proc := windows.NewCallback(wndProc)
		for _, name := range []*uint16{className, helperName} {
			wc := wndClassExW{WndProc: proc, Instance: a.instance, ClassName: name, Icon: windows.Handle(a.icon), IconSm: windows.Handle(a.icon)}
			wc.Size = uint32(unsafe.Sizeof(wc))
			wc.Cursor = loadCursor()
			if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
				regErr = err
			}
		}
	})
	if regErr != nil {
		return fmt.Errorf("webview: RegisterClassEx: %v", regErr)
	}
	a.helper, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(helperName)), 0, 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if a.helper == 0 {
		return errors.New("webview: could not create the helper window")
	}
	tc := windows.StringToUTF16Ptr("TaskbarCreated")
	r, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tc)))
	a.taskbar = uint32(r)
	setup(a)
	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	a.removeTray()
	return nil
}

func loadCursor() windows.Handle {
	r, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512 /* IDC_ARROW */)
	return windows.Handle(r)
}

func (a *winApp) scale(v int) uintptr { return uintptr(v * a.dpi / 96) }

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	a := theApp
	if a == nil {
		r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	if hwnd == a.helper {
		switch {
		case msg == wmDispatch:
			a.drain()
			return 0
		case msg == wmTray:
			switch lp & 0xffff {
			case wmLButtonUp:
				if a.tray != nil && a.tray.OnActivate != nil {
					a.tray.OnActivate()
				}
			case wmRButtonUp, wmContextMenu:
				a.showMenu()
			}
			return 0
		case a.taskbar != 0 && uint32(msg) == a.taskbar:
			if a.trayOn {
				a.trayOn = false
				a.SetTray(a.tray)
			}
			return 0
		}
	}
	if w := a.windows[hwnd]; w != nil {
		switch msg {
		case wmSize:
			if w.web != nil {
				w.web.Resize()
			}
			return 0
		case wmMove:
			if w.web != nil {
				w.web.NotifyParentWindowPositionChanged()
			}
		case wmClose:
			if w.opts.OnClose == nil || w.opts.OnClose() {
				pDestroyWindow.Call(hwnd)
			}
			return 0
		case wmDestroy:
			w.closed = true
			delete(a.windows, hwnd)
			return 0
		case wmGetMinMaxInfo:
			if w.opts.MinWidth > 0 || w.opts.MinHeight > 0 {
				mmi := *(**minMaxInfo)(unsafe.Pointer(&lp)) // lp is a pointer the system passes
				mmi.MinTrackSize = point{int32(a.scale(w.opts.MinWidth)), int32(a.scale(w.opts.MinHeight))}
			}
			return 0
		}
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (a *winApp) Dispatch(fn func()) {
	a.qmu.Lock()
	a.queue = append(a.queue, fn)
	a.qmu.Unlock()
	pPostMessageW.Call(a.helper, wmDispatch, 0, 0)
}

func (a *winApp) drain() {
	a.qmu.Lock()
	q := a.queue
	a.queue = nil
	a.qmu.Unlock()
	for _, fn := range q {
		fn()
	}
}

func (a *winApp) Quit() { a.Dispatch(func() { pPostQuitMessage.Call(0) }) }

func (a *winApp) NewWindow(o Options) (Window, error) {
	width, height := o.Size()
	title := windows.StringToUTF16Ptr(o.Title)
	style, ex := uintptr(wsOverlappedWindow), uintptr(0)
	x, y := uintptr(cwUseDefault), uintptr(cwUseDefault)
	if o.Overlay {
		// The bottom-right corner of the work area (above the task bar),
		// where notifications appear, away from the app's window.
		style, ex = wsPopup|wsBorder, wsExTopmost|wsExToolWindow|wsExNoActivate
		var wa struct{ Left, Top, Right, Bottom int32 }
		if r, _, _ := pSystemParametersInfoW.Call(0x0030 /* SPI_GETWORKAREA */, 0, uintptr(unsafe.Pointer(&wa)), 0); r == 0 {
			sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
			sh, _, _ := pGetSystemMetrics.Call(1 /* SM_CYSCREEN */)
			wa.Right, wa.Bottom = int32(sw), int32(sh)-int32(a.scale(48))
		}
		x = uintptr(wa.Right - int32(a.scale(width)) - int32(a.scale(overlayMargin)))
		y = uintptr(wa.Bottom - int32(a.scale(height)) - int32(a.scale(overlayMargin)))
	}
	hwnd, _, err := pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)), style,
		x, y, a.scale(width), a.scale(height), 0, 0, uintptr(a.instance), 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("webview: CreateWindowEx: %v", err)
	}
	w := &winWindow{app: a, hwnd: hwnd, opts: o}
	a.windows[hwnd] = w
	if !o.Hidden {
		if o.Overlay {
			pShowWindow.Call(hwnd, swShowNoActiv)
		} else {
			pShowWindow.Call(hwnd, swShow)
		}
	}
	web := edge.NewChromium()
	web.DataPath = a.cfg.DataDir
	if !web.Embed(hwnd) {
		pDestroyWindow.Call(hwnd)
		return nil, fmt.Errorf("%w: WebView2 could not start", ErrUnavailable)
	}
	if s, err := web.GetSettings(); err == nil {
		s.PutAreDefaultContextMenusEnabled(false)
		s.PutAreDevToolsEnabled(false)
		s.PutIsStatusBarEnabled(false)
		s.PutAreHostObjectsAllowed(false)
		s.PutIsWebMessageEnabled(false)
	}
	w.web = web
	web.Resize()
	web.Navigate(o.URL)
	return w, nil
}

func (w *winWindow) Navigate(url string) {
	if !w.closed {
		w.web.Navigate(url)
	}
}

func (w *winWindow) SetTitle(t string) {
	if !w.closed {
		pSetWindowTextW.Call(w.hwnd, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(t))))
	}
}

func (w *winWindow) Show() {
	if w.closed {
		return
	}
	if w.opts.Overlay {
		pShowWindow.Call(w.hwnd, swShowNoActiv)
		return
	}
	if r, _, _ := pIsIconic.Call(w.hwnd); r != 0 {
		pShowWindow.Call(w.hwnd, swRestore)
	} else {
		pShowWindow.Call(w.hwnd, swShow)
	}
	// Windows lets a background process take the foreground only in some
	// cases; when it refuses, the task bar button flashes instead.
	pAllowSetForegroundWindow.Call(uintptr(windows.GetCurrentProcessId()))
	pBringWindowToTop.Call(w.hwnd)
	if r, _, _ := pSetForegroundWindow.Call(w.hwnd); r == 0 {
		pFlashWindow.Call(w.hwnd, 1)
	}
	if w.web != nil {
		w.web.Focus()
	}
}

func (w *winWindow) Hide() {
	if !w.closed {
		pShowWindow.Call(w.hwnd, swHide)
	}
}

func (w *winWindow) Close() {
	if !w.closed {
		pDestroyWindow.Call(w.hwnd)
	}
}

func (a *winApp) nid() notifyIconData {
	n := notifyIconData{Wnd: a.helper, ID: 1}
	n.Size = uint32(unsafe.Sizeof(n))
	return n
}

func (a *winApp) SetTray(t *Tray) error {
	if t == nil {
		a.removeTray()
		a.tray = nil
		return nil
	}
	a.tray = t
	n := a.nid()
	n.Flags = nifMessage | nifIcon | nifTip
	n.CallbackMessage = wmTray
	n.Icon = windows.Handle(a.icon)
	tip, _ := windows.UTF16FromString(t.Tooltip)
	copy(n.Tip[:len(n.Tip)-1], tip)
	op := uintptr(nimModify)
	if !a.trayOn {
		op = nimAdd
	}
	if r, _, err := pShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&n))); r == 0 {
		return fmt.Errorf("webview: Shell_NotifyIcon: %v", err)
	}
	a.trayOn = true
	return nil
}

func (a *winApp) removeTray() {
	if !a.trayOn {
		return
	}
	n := a.nid()
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&n)))
	a.trayOn = false
}

func (a *winApp) showMenu() {
	if a.tray == nil {
		return
	}
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)
	items := a.tray.Menu
	for i, m := range items {
		if m.Label == "" {
			pAppendMenuW.Call(menu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if m.Disabled {
			flags |= mfGrayed
		}
		pAppendMenuW.Call(menu, flags, uintptr(i+1), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(m.Label))))
	}
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// The menu closes when the user clicks elsewhere only if its owner is
	// the foreground window (a documented quirk of TrackPopupMenu).
	pSetForegroundWindow.Call(a.helper)
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmReturn|tpmRight, uintptr(pt.X), uintptr(pt.Y), 0, a.helper, 0)
	pPostMessageW.Call(a.helper, wmNull, 0, 0)
	if i := int(cmd) - 1; i >= 0 && i < len(items) && items[i].OnClick != nil && !items[i].Disabled {
		items[i].OnClick()
	}
}
