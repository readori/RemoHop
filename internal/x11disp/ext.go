package x11disp

import (
	"errors"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// EnableRandR enables RandR requests on c. randr.Init would also register
// RandR's event and error decoders in xgb's process-wide tables: a map
// write without a lock, racing with every other X connection of this
// process that is decoding events or opening RandR at the same time (the
// clipboard, desktop, screenshot and input services each hold some), which
// the Go runtime turns into a fatal "concurrent map writes". Listing
// monitors needs no RandR events, so only the extension's opcode is
// recorded on c.
func EnableRandR(c *xgb.Conn) error {
	rep, err := xproto.QueryExtension(c, 5, "RANDR").Reply()
	if err != nil {
		return err
	}
	if !rep.Present {
		return errors.New("no RANDR extension")
	}
	c.ExtLock.Lock()
	c.Extensions["RANDR"] = rep.MajorOpcode
	c.ExtLock.Unlock()
	return nil
}
