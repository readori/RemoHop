//go:build !windows

package service

import (
	"context"
	"errors"
	"io"
)

var errNotWindows = errors.New("Windows services are only available on Windows")

type winService struct{}

func newWindows() backend { return winService{} }

func (winService) Print(Options) (string, error)                        { return "", errNotWindows }
func (winService) Install(context.Context, Options) (*Installed, error) { return nil, errNotWindows }
func (winService) Uninstall(context.Context, Options) (bool, error)     { return false, errNotWindows }
func (winService) Control(context.Context, Options, string) error       { return errNotWindows }
func (winService) Status(context.Context, Options) (*Status, error)     { return nil, errNotWindows }
func (winService) Logs(context.Context, Options, int, bool, io.Writer) error {
	return errNotWindows
}

func runAsService(context.Context, string, []string, bool, io.Writer) error { return errNotWindows }

func openServiceLog(string) (io.WriteCloser, error) { return nil, errNotWindows }
