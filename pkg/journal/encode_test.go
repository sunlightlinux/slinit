package journal

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// encodeCases are events whose encoding must equal json.Marshal's:
// every omitempty field empty and set, negative numbers, map key
// order, and each class of string escaping.
func encodeCases() []*Event {
	all := &Event{
		Ts: 1764200000123456789, Mts: 42, Msg: "hello", Prio: PriorityDebug,
		Unit: "sshd", Namespace: "ns", SyslogIdentifier: "id",
		Fields:    map[string]string{"B": "2", "A": "1", "C_3": "3"},
		Transport: TransportStdout, Pid: 1, Uid: 1000, Gid: 100,
		Comm: "c", Exe: "/bin/e", Cmdline: "e -x", BootID: "b", MachineID: "m", Hostname: "h",
	}
	return []*Event{
		{},
		all,
		{Ts: -1, Mts: -2, Prio: -3, Pid: -1, Uid: -4, Gid: -5},
		{Ts: 1, Fields: map[string]string{}},
		{Ts: 1, Fields: map[string]string{"ONE": "x"}},
		{Ts: 1, Fields: map[string]string{"Z": "z", "A": "a"}},
		{Ts: 1, Fields: map[string]string{"A": "a", "Z": "z"}},
		{Ts: 1, Fields: map[string]string{"": "empty key", "k<&>": "v\"\\"}},
		{Msg: "ctl \x00\x01\x07\b\f\n\r\t\x1f\x7f end"},
		{Msg: `quote " backslash \ html <a href="x">&amp;</a>`},
		{Msg: "utf8 \u00e9\u4e2d\U0001F600 seps \u2028\u2029 bom \ufeff"},
		{Msg: "bad \xff\xfe utf8 \xc3 trunc \xe2\x82 surrogate \xed\xa0\x80 end\xc3"},
		{Unit: "\xff", Namespace: "\u2028", SyslogIdentifier: "<", Comm: "\n", Exe: "&", Cmdline: "\"", BootID: "\\", MachineID: "\x00", Hostname: ">", Transport: "\u2029"},
		{Msg: strings.Repeat("x", 70000)},
	}
}

func TestAppendJSONMatchesMarshal(t *testing.T) {
	for i, e := range encodeCases() {
		want, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.appendJSON(nil); !bytes.Equal(got, want) {
			t.Errorf("case %d:\n got %q\nwant %q", i, got, want)
		}
		got, err := e.MarshalJSONL()
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("case %d: MarshalJSONL = %q, %v; want %q", i, got, err, want)
		}
	}
}

// appendJSON writes Event's fields by hand; one added to Event
// without it would vanish from the wire. This lists what it encodes.
func TestAppendJSONCoversEveryField(t *testing.T) {
	encoded := []string{
		"ts", "mts", "msg,omitempty", "prio,omitempty", "unit,omitempty",
		"namespace,omitempty", "syslog_identifier,omitempty", "fields,omitempty",
		"_transport,omitempty", "_pid,omitempty", "_uid,omitempty", "_gid,omitempty",
		"_comm,omitempty", "_exe,omitempty", "_cmdline,omitempty",
		"_boot_id,omitempty", "_machine_id,omitempty", "_hostname,omitempty",
	}
	typ := reflect.TypeOf(Event{})
	var tags []string
	for i := 0; i < typ.NumField(); i++ {
		tags = append(tags, typ.Field(i).Tag.Get("json"))
	}
	if !reflect.DeepEqual(tags, encoded) {
		t.Fatalf("Event's json fields changed; update appendJSON and this list:\n have %q\n encoded %q", tags, encoded)
	}
}

func FuzzAppendJSONMatchesMarshal(f *testing.F) {
	f.Add("msg", "unit", "K", "v", "K2", "v2", int64(1), 3, "h")
	f.Add("\xff<\u2028\x00", "", "", "", "A", "\"", int64(-7), -1, "\\")
	f.Fuzz(func(t *testing.T, msg, unit, k1, v1, k2, v2 string, ts int64, n int, s string) {
		e := &Event{
			Ts: ts, Mts: ts / 3, Msg: msg, Prio: Priority(n), Unit: unit,
			Namespace: s, SyslogIdentifier: v2, Transport: Transport(k1),
			Pid: n, Uid: -n, Gid: n * 7, Comm: v1, Exe: k2, Cmdline: s + msg,
			BootID: unit, MachineID: k1, Hostname: s,
		}
		switch n & 3 {
		case 1:
			e.Fields = map[string]string{k1: v1}
		case 2:
			e.Fields = map[string]string{k1: v1, k2: v2}
		case 3:
			e.Fields = map[string]string{k1: v1, k2: v2, s: msg, unit: ""}
		}
		want, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.appendJSON(nil); !bytes.Equal(got, want) {
			t.Fatalf("\n got %q\nwant %q", got, want)
		}
	})
}
