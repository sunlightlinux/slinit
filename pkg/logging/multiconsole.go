package logging

import (
	"io"
	"os"
	"strings"
	"syscall"
)

// OpenSecondaryConsoles opens every enabled kernel console listed in
// /proc/consoles except the one /dev/console already maps to (the CON_CONSDEV
// "C" entry), returning a writer that fans out to them and the files to close.
//
// slinit's boot console writes to /dev/console, which on Linux is only the
// *last* console= on the kernel command line. With "console=tty0
// console=ttyS0" /dev/console is the serial line, so the "[ OK ] name" boot
// list never reaches the VGA screen (tty0). Mirroring to the secondary
// consoles puts the list on every physical console — serial *and* VGA —
// without printing twice on the primary.
//
// Returns (nil, nil) when there is no secondary console or /proc is
// unavailable; callers treat that as "nothing to mirror".
func OpenSecondaryConsoles() (io.Writer, []io.Closer) {
	data, err := os.ReadFile("/proc/consoles")
	if err != nil {
		return nil, nil
	}
	var writers []io.Writer
	var closers []io.Closer
	for _, line := range strings.Split(string(data), "\n") {
		name := secondaryConsoleName(line)
		if name == "" {
			continue
		}
		f, err := os.OpenFile("/dev/"+name, os.O_WRONLY|syscall.O_NOCTTY, 0)
		if err != nil {
			continue
		}
		writers = append(writers, f)
		closers = append(closers, f)
	}
	if len(writers) == 0 {
		return nil, nil
	}
	return io.MultiWriter(writers...), closers
}

// secondaryConsoleName parses one /proc/consoles line and returns the device
// name (e.g. "tty0") when the console is enabled ('E') but is NOT the
// /dev/console target ('C'); otherwise it returns "". The flag field is
// fixed-width with spaces for unset flags, e.g.
//
//	ttyS0                -W- (EC  p  )    4:64
//
// so the flags are read from between the parentheses, not by whitespace.
func secondaryConsoleName(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	name := fields[0]
	lp := strings.IndexByte(line, '(')
	rp := strings.IndexByte(line, ')')
	if lp < 0 || rp <= lp {
		return ""
	}
	flags := line[lp+1 : rp]
	if !strings.ContainsRune(flags, 'E') { // not enabled
		return ""
	}
	if strings.ContainsRune(flags, 'C') { // this IS /dev/console; skip it
		return ""
	}
	return name
}
