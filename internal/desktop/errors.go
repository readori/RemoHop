package desktop

import "github.com/readori/RemoHop/internal/localui"

// Errorf is a refusal the page explains in its own language (its err_<code>
// text); msg is the English one.
func Errorf(code, msg string) error { return localui.Errorf(code, msg) }
