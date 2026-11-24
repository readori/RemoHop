package web

import (
	"encoding/json"
	"io"
)

func newJSONDecoder(r io.Reader) *json.Decoder {
	d := json.NewDecoder(r)
	return d
}
