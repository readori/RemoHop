package desktop

import "os"

// AutostartState is "start RemoHop when I sign in".
type AutostartState struct {
	Available bool `json:"available"`
	On        bool `json:"on"`
}

// autostartArgs start the app in the tray, without its window.
var autostartArgs = []string{"app", "--hidden"}

func autostartState() AutostartState {
	exe, err := os.Executable()
	if err != nil {
		return AutostartState{}
	}
	on, ok := autostartOn(exe)
	return AutostartState{Available: ok, On: on}
}

func setAutostart(on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return writeAutostart(exe, on)
}
