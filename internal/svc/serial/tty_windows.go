//go:build windows

package serial

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// winPort is a COM port. UNVERIFIED: compile-checked only.
type winPort struct {
	h      windows.Handle
	mu     sync.Mutex
	closed bool
}

const (
	dcbBinary      = 0x0001
	dcbParity      = 0x0002
	dcbOutxCTS     = 0x0004
	dcbDTRMask     = 0x0030 // fDtrControl bits 4-5
	dcbDTREnable   = 0x0010
	dcbRTSMask     = 0x3000 // fRtsControl bits 12-13
	dcbRTSEnable   = 0x1000
	dcbRTSHS       = 0x2000
	mszCTS, mszDSR = 0x10, 0x20
	mszRI, mszDCD  = 0x40, 0x80
)

func openDevice(path string, cfg Config) (Port, error) {
	name, err := windows.UTF16PtrFromString(`\\.\` + path)
	if err != nil {
		return nil, err
	}
	// Share mode 0 gives exclusive access: a second open fails.
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	p := &winPort{h: h}
	to := windows.CommTimeouts{ReadIntervalTimeout: 0xFFFFFFFF, ReadTotalTimeoutMultiplier: 0xFFFFFFFF, ReadTotalTimeoutConstant: 100}
	if err := windows.SetCommTimeouts(h, &to); err != nil {
		p.Close()
		return nil, err
	}
	if err := p.Apply(cfg); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *winPort) Apply(cfg Config) error {
	var d windows.DCB
	d.DCBlength = uint32(28)
	if err := windows.GetCommState(p.h, &d); err != nil {
		return err
	}
	d.BaudRate = uint32(cfg.Baud)
	d.ByteSize = uint8(cfg.Data)
	d.Flags |= dcbBinary
	d.Flags &^= dcbRTSMask | dcbOutxCTS
	if cfg.RTSCTS {
		d.Flags |= dcbRTSHS | dcbOutxCTS
	} else {
		d.Flags |= dcbRTSEnable
	}
	switch cfg.Parity {
	case 'E':
		d.Parity, d.Flags = 2, d.Flags|dcbParity
	case 'O':
		d.Parity, d.Flags = 1, d.Flags|dcbParity
	default:
		d.Parity, d.Flags = 0, d.Flags&^dcbParity
	}
	if cfg.Stop == 2 {
		d.StopBits = 2
	} else {
		d.StopBits = 0
	}
	return windows.SetCommState(p.h, &d)
}

func (p *winPort) Read(b []byte) (int, error) {
	for {
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return 0, errors.New("port closed")
		}
		var n uint32
		if err := windows.ReadFile(p.h, b, &n, nil); err != nil {
			return 0, err
		}
		if n > 0 {
			return int(n), nil
		}
	}
}

func (p *winPort) Write(b []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(p.h, b, &n, nil)
	return int(n), err
}

func (p *winPort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	// The reader polls every 100ms; give it a chance to leave ReadFile.
	go func(h windows.Handle) { time.Sleep(300 * time.Millisecond); windows.CloseHandle(h) }(p.h)
	return nil
}

func (p *winPort) SetModem(dtr, rts *bool) error {
	esc := func(v *bool, on, off uint32) error {
		if v == nil {
			return nil
		}
		f := off
		if *v {
			f = on
		}
		return windows.EscapeCommFunction(p.h, f)
	}
	const setRTS, clrRTS, setDTR, clrDTR = 3, 4, 5, 6
	if err := esc(dtr, setDTR, clrDTR); err != nil {
		return err
	}
	return esc(rts, setRTS, clrRTS)
}

func (p *winPort) Modem() (Modem, error) {
	var s uint32
	if err := windows.GetCommModemStatus(p.h, &s); err != nil {
		return Modem{}, err
	}
	return Modem{CTS: s&mszCTS != 0, DSR: s&mszDSR != 0, DCD: s&mszDCD != 0, RI: s&mszRI != 0}, nil
}

func (p *winPort) Break(d time.Duration) error {
	const setBreak, clrBreak = 8, 9
	if err := windows.EscapeCommFunction(p.h, setBreak); err != nil {
		return err
	}
	time.Sleep(d)
	return windows.EscapeCommFunction(p.h, clrBreak)
}
