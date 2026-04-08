package fuzz

import (
	"testing"

	"github.com/sunlightlinux/slinit/internal/util"
	"github.com/sunlightlinux/slinit/pkg/process"
)

// FuzzParseCapabilities fuzzes the Linux capability name parser.
func FuzzParseCapabilities(f *testing.F) {
	f.Add("cap_net_bind_service")
	f.Add("cap_sys_ptrace cap_net_raw")
	f.Add("net_bind_service,sys_ptrace")
	f.Add("CAP_NET_BIND_SERVICE")
	f.Add("0 1 2 3")
	f.Add("")
	f.Add("invalid_cap")
	f.Add("cap_")
	f.Add("cap_net_bind_service , , cap_sys_admin")
	f.Add("999")

	f.Fuzz(func(t *testing.T, data string) {
		process.ParseCapabilities(data)
	})
}

// FuzzParseSecurebits fuzzes the securebits flag parser.
func FuzzParseSecurebits(f *testing.F) {
	f.Add("noroot")
	f.Add("noroot noroot-locked")
	f.Add("no-setuid-fixup no-setuid-fixup-locked")
	f.Add("keep-caps keep-caps-locked")
	f.Add("")
	f.Add("invalid-bit")
	f.Add("noroot noroot noroot") // duplicates

	f.Fuzz(func(t *testing.T, data string) {
		process.ParseSecurebits(data)
	})
}

// FuzzParseDuration fuzzes the decimal seconds duration parser.
func FuzzParseDuration(f *testing.F) {
	f.Add("10")
	f.Add("0.5")
	f.Add("0")
	f.Add("3600")
	f.Add("")
	f.Add("abc")
	f.Add("-1")
	f.Add("1e10")
	f.Add("99999999999999999999999")
	f.Add("0.001")
	f.Add("inf")
	f.Add("NaN")

	f.Fuzz(func(t *testing.T, data string) {
		util.ParseDuration(data)
	})
}

// FuzzParseSignal fuzzes the signal name/number parser.
func FuzzParseSignal(f *testing.F) {
	f.Add("SIGTERM")
	f.Add("TERM")
	f.Add("9")
	f.Add("HUP")
	f.Add("SIGUSR1")
	f.Add("")
	f.Add("SIGNOTREAL")
	f.Add("999")
	f.Add("-1")
	f.Add("sigkill")

	f.Fuzz(func(t *testing.T, data string) {
		util.ParseSignal(data)
	})
}
