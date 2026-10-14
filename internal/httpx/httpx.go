// Package httpx holds small helpers for talking to third-party HTTP APIs.
package httpx

import (
	"encoding/json"
	"encoding/xml"
	"io"
)

// MaxBody is the default cap for a decoded API response (generous for JSON/XML metadata, small
// enough that a hostile or broken upstream cannot make the bot buffer gigabytes).
const MaxBody = 4 << 20

// DecodeJSON decodes at most max bytes of r into v; a longer body fails as truncated JSON.
func DecodeJSON(r io.Reader, max int64, v any) error {
	return json.NewDecoder(io.LimitReader(r, max)).Decode(v)
}

// DecodeXML is DecodeJSON for XML.
func DecodeXML(r io.Reader, max int64, v any) error {
	return xml.NewDecoder(io.LimitReader(r, max)).Decode(v)
}
