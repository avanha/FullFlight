// Package decoder turns raw ACARS application text into structured JSON using libacars.
package decoder

/*
#cgo pkg-config: libacars-2
#cgo LDFLAGS: -Wl,-rpath,${SRCDIR}/../../third_party/install/lib
#include <stdlib.h>
#include <libacars/libacars.h>
#include <libacars/acars.h>
#include <libacars/vstring.h>
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"strings"
	"unsafe"
)

type Direction int

const (
	Unknown  Direction = Direction(C.LA_MSG_DIR_UNKNOWN)
	Uplink   Direction = Direction(C.LA_MSG_DIR_GND2AIR)
	Downlink Direction = Direction(C.LA_MSG_DIR_AIR2GND)
)

// Decoded is libacars' output for one message.
type Decoded struct {
	Tree map[string]any // the JSON tree
	Text string         // libacars' human-readable rendering
}

// DecodeApps decodes ACARS application text (already stripped of any
// message-number/flight-ID prefix) with the given label and direction.
// It returns libacars' JSON tree, or nil if libacars recognised nothing.
func DecodeApps(label, text string, dir Direction) (map[string]any, error) {
	d, err := DecodeAppsFull(label, text, dir)
	if d == nil {
		return nil, err
	}
	return d.Tree, err
}

// DecodeAppsFull is DecodeApps plus the text rendering. It returns nil, nil if
// libacars recognised nothing.
func DecodeAppsFull(label, text string, dir Direction) (*Decoded, error) {
	cl, ct := C.CString(label), C.CString(text)
	defer C.free(unsafe.Pointer(cl))
	defer C.free(unsafe.Pointer(ct))

	root := C.la_acars_decode_apps(cl, ct, C.la_msg_dir(dir))
	if root == nil {
		return nil, nil
	}
	defer C.la_proto_tree_destroy(root)

	vs := C.la_proto_tree_format_json(nil, root)
	if vs == nil {
		return nil, fmt.Errorf("libacars: JSON formatting failed")
	}
	defer C.la_vstring_destroy(vs, C.bool(true))
	var out Decoded
	if err := json.Unmarshal([]byte(C.GoString(vs.str)), &out.Tree); err != nil {
		return nil, fmt.Errorf("libacars: bad JSON: %w", err)
	}

	if tv := C.la_proto_tree_format_text(nil, root); tv != nil {
		out.Text = strings.TrimSpace(C.GoString(tv.str))
		C.la_vstring_destroy(tv, C.bool(true))
	}
	return &out, nil
}
