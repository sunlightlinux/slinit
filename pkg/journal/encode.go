package journal

import (
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"
)

// appendJSON appends the event's JSON encoding to dst: the bytes
// json.Marshal(e) produces, field for field, without its reflection,
// its per-map key slice and the copy it returns. Every event is
// encoded once per datagram to slinit-journald, and json.Marshal was
// the largest part of an emit's cost. TestAppendJSONMatchesMarshal and
// FuzzAppendJSONMatchesMarshal hold the output to json.Marshal's, and
// TestAppendJSONCoversEveryField fails when Event gains a field this
// does not encode.
func (e *Event) appendJSON(dst []byte) []byte {
	dst = append(dst, `{"ts":`...)
	dst = strconv.AppendInt(dst, e.Ts, 10)
	dst = append(dst, `,"mts":`...)
	dst = strconv.AppendInt(dst, e.Mts, 10)
	dst = appendStringField(dst, `,"msg":`, e.Msg)
	dst = appendIntField(dst, `,"prio":`, int64(e.Prio))
	dst = appendStringField(dst, `,"unit":`, e.Unit)
	dst = appendStringField(dst, `,"namespace":`, e.Namespace)
	dst = appendStringField(dst, `,"syslog_identifier":`, e.SyslogIdentifier)
	if len(e.Fields) > 0 {
		dst = append(dst, `,"fields":{`...)
		dst = appendSortedFields(dst, e.Fields)
		dst = append(dst, '}')
	}
	dst = appendStringField(dst, `,"_transport":`, string(e.Transport))
	dst = appendIntField(dst, `,"_pid":`, int64(e.Pid))
	dst = appendIntField(dst, `,"_uid":`, int64(e.Uid))
	dst = appendIntField(dst, `,"_gid":`, int64(e.Gid))
	dst = appendStringField(dst, `,"_comm":`, e.Comm)
	dst = appendStringField(dst, `,"_exe":`, e.Exe)
	dst = appendStringField(dst, `,"_cmdline":`, e.Cmdline)
	dst = appendStringField(dst, `,"_boot_id":`, e.BootID)
	dst = appendStringField(dst, `,"_machine_id":`, e.MachineID)
	dst = appendStringField(dst, `,"_hostname":`, e.Hostname)
	return append(dst, '}')
}

// appendStringField appends key and s, unless s is empty (omitempty).
func appendStringField(dst []byte, key, s string) []byte {
	if s == "" {
		return dst
	}
	return appendJSONString(append(dst, key...), s)
}

// appendIntField appends key and n, unless n is zero (omitempty).
func appendIntField(dst []byte, key string, n int64) []byte {
	if n == 0 {
		return dst
	}
	return strconv.AppendInt(append(dst, key...), n, 10)
}

// appendSortedFields appends m's pairs in key order, as json.Marshal
// sorts a map's keys. One or two keys - the usual event - need no
// slice.
func appendSortedFields(dst []byte, m map[string]string) []byte {
	if len(m) <= 2 {
		var k0, k1 string
		n := 0
		for k := range m {
			if n == 0 {
				k0 = k
			} else {
				k1 = k
			}
			n++
		}
		if n == 2 && k1 < k0 {
			k0, k1 = k1, k0
		}
		dst = appendJSONString(dst, k0)
		dst = appendJSONString(append(dst, ':'), m[k0])
		if n == 2 {
			dst = appendJSONString(append(dst, ','), k1)
			dst = appendJSONString(append(dst, ':'), m[k1])
		}
		return dst
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i, k := range keys {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendJSONString(dst, k)
		dst = appendJSONString(append(dst, ':'), m[k])
	}
	return dst
}

const jsonHex = "0123456789abcdef"

// jsonSafe reports whether json.Marshal writes the ASCII byte b as
// itself: anything but a control character, '"', '\\', and the HTML
// characters '<', '>' and '&' it escapes by default (its htmlSafeSet).
func jsonSafe(b byte) bool {
	return b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&'
}

// appendJSONString appends s as a JSON string exactly as json.Marshal
// does with HTML escaping on (encoding/json appendString): the same
// short escapes, \u00XX for other control characters and <, >, &,
// \ufffd for each byte of invalid UTF-8, and \u2028 / \u2029 escaped.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if jsonSafe(b) {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', jsonHex[b>>4], jsonHex[b&0xF])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, "\\ufffd"...)
			i += size
			start = i
			continue
		}
		if c == '\u2028' || c == '\u2029' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', jsonHex[c&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// sendBufPool holds the buffers publishToSocket encodes into. A send
// copies the datagram into the socket, so the buffer is free again
// once the write returns.
var sendBufPool = sync.Pool{New: func() any { b := make([]byte, 0, 1024); return &b }}

// maxPooledSendBuf bounds the buffers kept in sendBufPool: one huge
// event must not pin its buffer for the life of PID 1.
const maxPooledSendBuf = 64 * 1024
