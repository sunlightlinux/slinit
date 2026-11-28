package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// A service's .socket unit.
//
// systemd ships many daemons as a pair: foo.socket holds the listening
// sockets, foo.service the daemon that is handed them (LISTEN_FDS). A
// daemon written for that (cups, pcscd, avahi, sshd's socket mode)
// finds no socket of its own to listen on when started without them.
// slinit has the same activation built into the service
// (socket-listen, socket-permissions, socket-uid/gid, socket-reuseport),
// so the loader reads the pair's socket unit with the service and maps
// it there: foo.socket, or the units the service's Sockets= names,
// with the socket unit's drop-ins, from the unit's own directory or the
// unit directories.
//
// The daemon is started with its sockets (socket-activation =
// immediate): systemd starts it on the first connection, but the
// socket unit is the one usually enabled and the daemon then has to
// come up anyway; socket-activation = on-demand gives the lazy start in
// an override. Accept=yes pairs a socket with a template foo@.service,
// one instance per connection, which slinit loads as no service name;
// that socket and the listener kinds slinit cannot open are reported
// and left out, as is everything about the socket slinit does not do.

// sdSocketUnits are the socket units of the service: those its
// Sockets= named, in order, else foo.socket.
func sdSocketUnits(cfg *SystemdConfig) (names []string, explicit bool) {
	if len(cfg.socketUnits) > 0 {
		return cfg.socketUnits, true
	}
	return []string{cfg.svcName + ".socket"}, false
}

// findUnitFile returns the first file named name in dirs (in priority
// order), or "".
func findUnitFile(name string, dirs []string) string {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// applySystemdSockets maps the service's socket units onto its
// socket-* settings.
func applySystemdSockets(cfg *SystemdConfig, unitDirs []string) []SystemdWarning {
	dirs := append(append([]string(nil), unitDirs...), filepath.Dir(cfg.unitPath))
	names, explicit := sdSocketUnits(cfg)
	var warns []SystemdWarning
	var listens []string
	var opts sdSocketOpts
	for _, name := range names {
		path := findUnitFile(name, dirs)
		if path == "" {
			if explicit {
				warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("Sockets=%s: no such socket unit — the service is started without it", name)})
			}
			continue
		}
		s, w := readSystemdSocket(cfg, path, name, unitDirs)
		warns = append(warns, w...)
		if s == nil {
			continue
		}
		listens = append(listens, s.listens...)
		opts.merge(s.opts, name, &warns)
	}
	if len(listens) == 0 {
		return warns
	}
	for i, l := range listens {
		key := "socket-listen"
		if i > 0 {
			key = "socket-listen+"
		}
		cfg.mapped = append(cfg.mapped, kvPair{key, l})
	}
	// systemd's SocketMode= defaults to 0666, slinit's
	// socket-permissions to 0600: a socket unit that relies on the
	// default (cups.socket, polkit-agent-helper.socket) is for clients
	// that are not root, which 0600 would turn away.
	if opts.mode == "" {
		opts.mode = "0666"
	}
	cfg.setMapped("socket-permissions", opts.mode)
	if opts.uid != "" {
		cfg.setMapped("socket-uid", opts.uid)
	}
	if opts.gid != "" {
		cfg.setMapped("socket-gid", opts.gid)
	}
	if opts.reusePort {
		cfg.setMapped("socket-reuseport", "yes")
	}
	if opts.accept {
		cfg.setMapped("socket-accept", "yes")
	}
	return warns
}

// SystemdAcceptTemplate returns foo@.service when foo.socket in dirs has
// Accept=yes and the template exists beside it: what systemd starts per
// connection on that socket, and what loading the name foo means when
// there is no foo.service.
func SystemdAcceptTemplate(dirs []string, name string) (string, bool) {
	// The first entry of each in priority order, as systemd takes it: a
	// mask (a symlink to /dev/null, an empty file) in /etc hides the
	// copy in /usr/lib rather than being stepped over.
	sock := firstUnitEntry(name+".socket", dirs)
	tmpl := firstUnitEntry(name+"@.service", dirs)
	if sock == "" || tmpl == "" || systemdUnitMasked(sock) || systemdUnitMasked(tmpl) {
		return "", false
	}
	// Accept= as the socket unit and its drop-ins leave it.
	accept := false
	for _, f := range append([]string{sock}, systemdDropins(dirs, name+".socket")...) {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", false
		}
		section := ""
		for _, raw := range joinSystemdLines(string(b)) {
			line := strings.TrimSpace(raw)
			if m := sectionRe.FindStringSubmatch(line); m != nil {
				section = m[1]
				continue
			}
			if m := kvRe.FindStringSubmatch(line); m != nil && section == "Socket" && m[1] == "Accept" {
				accept = truthy(strings.TrimSpace(m[2]))
			}
		}
	}
	return tmpl, accept
}

// firstUnitEntry returns the first name in dirs that exists at all, a
// symlink to /dev/null included, or "".
func firstUnitEntry(name string, dirs []string) string {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Lstat(p); err == nil {
			return p
		}
	}
	return ""
}

// sdSocketOpts are the [Socket] settings that apply to every listener.
type sdSocketOpts struct {
	mode, uid, gid string
	reusePort      bool
	accept         bool
}

// merge folds the settings of another socket unit of the same service
// in; slinit has one set for all its listeners, so two units that
// disagree keep the first one's, with a warning.
func (o *sdSocketOpts) merge(n sdSocketOpts, unit string, warns *[]SystemdWarning) {
	pick := func(what string, cur *string, v string) {
		switch {
		case v == "" || v == *cur:
		case *cur == "":
			*cur = v
		default:
			*warns = append(*warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %s %s differs from an earlier socket unit's %s, which slinit applies to every listener", unit, what, v, *cur)})
		}
	}
	pick("SocketMode", &o.mode, n.mode)
	pick("SocketUser", &o.uid, n.uid)
	pick("SocketGroup", &o.gid, n.gid)
	o.reusePort = o.reusePort || n.reusePort
	o.accept = o.accept || n.accept
}

type sdSocket struct {
	listens []string
	opts    sdSocketOpts
}

// sdSocketIgnored are [Socket] settings that change nothing slinit has
// to reproduce for the daemon to work, or that only concern systemd's
// own bookkeeping; they are left out without a word.
var sdSocketIgnored = map[string]bool{
	"Backlog": true, "BindIPv6Only": true, "DirectoryMode": true, "RemoveOnStop": true,
	"MaxConnections": true, "MaxConnectionsPerSource": true, "KeepAlive": true,
	"KeepAliveTimeSec": true, "KeepAliveIntervalSec": true, "KeepAliveProbes": true,
	"NoDelay": true, "Priority": true, "ReceiveBuffer": true, "SendBuffer": true,
	"IPTOS": true, "IPTTL": true, "Mark": true, "TriggerLimitIntervalSec": true,
	"TriggerLimitBurst": true, "PollLimitIntervalSec": true, "PollLimitBurst": true,
	"DeferAcceptSec": true, "DeferTrigger": true, "DeferTriggerMaxSec": true,
	"TimeoutSec": true, "Timestamping": true, "Writable": true, "FlushPending": true,
	"PipeSize": true, "MessageQueueMaxMessages": true, "MessageQueueMessageSize": true,
	"FreeBind": true, "Transparent": true, "Broadcast": true,
}

// readSystemdSocket reads one socket unit and its drop-ins. It returns
// nil when the unit is for another service or cannot apply here.
func readSystemdSocket(cfg *SystemdConfig, path, name string, unitDirs []string) (*sdSocket, []SystemdWarning) {
	files := []string{path}
	files = append(files, systemdDropins(unitDirs, name)...)
	s := &sdSocket{}
	var warns []SystemdWarning
	accept := false
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, []SystemdWarning{{"WARN", fmt.Sprintf("%s: %v — the service is started without it", name, err)}}
		}
		section := ""
		for _, raw := range joinSystemdLines(string(b)) {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if m := sectionRe.FindStringSubmatch(line); m != nil {
				section = m[1]
				continue
			}
			m := kvRe.FindStringSubmatch(line)
			if m == nil || section != "Socket" {
				continue
			}
			key, val := m[1], strings.TrimSpace(trimQuotes(strings.TrimSpace(m[2])))
			switch key {
			case "ListenStream", "ListenDatagram":
				if val == "" {
					s.listens = nil // an empty assignment resets the list
					continue
				}
				l, err := sdListenSpec(key, val)
				if err != nil {
					warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %s=%s left out: %v", name, key, val, err)})
					continue
				}
				s.listens = append(s.listens, l)
			case "ListenSequentialPacket", "ListenFIFO", "ListenSpecial", "ListenNetlink",
				"ListenMessageQueue", "ListenUSBFunction":
				warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %s=%s left out: slinit opens only stream and datagram sockets", name, key, val)})
			case "Accept":
				accept = truthy(val)
			case "Service":
				if strings.TrimSuffix(val, ".service") != cfg.svcName {
					return nil, []SystemdWarning{{"NOTE", fmt.Sprintf("%s is for %s, not this service: not read", name, val)}}
				}
			case "SocketMode":
				if _, err := strconv.ParseUint(val, 8, 32); err != nil {
					warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: SocketMode=%s is no octal mode — ignored, as systemd does", name, val)})
					continue
				}
				s.opts.mode = val
			case "SocketUser", "SocketGroup":
				id, err := sdLookupID(key, val)
				if err != nil {
					warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %s=%s ignored: %v; the socket keeps slinit's ownership", name, key, val, err)})
					continue
				}
				if key == "SocketUser" {
					s.opts.uid = id
				} else {
					s.opts.gid = id
				}
				if id != val {
					// A converted file carries the number: right on this
					// host, worth checking on another.
					warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s: %s=%s is %s on this host", name, key, val, id)})
				}
			case "ReusePort":
				s.opts.reusePort = truthy(val)
			case "FileDescriptorName":
				warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s: FileDescriptorName=%s not carried: slinit passes socket-listen descriptors without names (LISTEN_FDNAMES)", name, val)})
			case "Symlinks", "PassCredentials", "PassSecurity", "PassPacketInfo", "PassFileDescriptorsToExec",
				"SmackLabel", "SmackLabelIPIn", "SmackLabelIPOut", "SELinuxContextFromNet", "BindToDevice",
				"ExecStartPre", "ExecStartPost", "ExecStopPre", "ExecStopPost", "TCPCongestion", "ReusePortGroup":
				warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s: %s=%s has no slinit equivalent", name, key, val)})
			default:
				if !sdSocketIgnored[key] {
					warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s: %s=%s not carried", name, key, val)})
				}
			}
		}
	}
	if accept != cfg.acceptTmpl {
		if accept {
			return nil, append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s has Accept=yes, which starts an instance of %s@.service per connection; "+
				"slinit does not read it for %s.service — set socket-accept on the service instead", name, cfg.svcName, cfg.svcName)})
		}
		return nil, append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s has no Accept=yes, so it is not for the template %s@.service", name, cfg.svcName)})
	}
	if accept {
		s.opts.accept = true
	}
	if len(s.listens) == 0 {
		return nil, warns
	}
	warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s read: the service is started with its sockets (%s)", name, strings.Join(s.listens, " "))})
	return s, warns
}

// sdListenSpec turns a systemd socket address into a socket-listen
// value: an absolute path is a Unix stream socket; a port, or host and
// port, is tcp: or udp: (a port alone listens on every address, as
// systemd's does).
func sdListenSpec(key, val string) (string, error) {
	stream := key == "ListenStream"
	switch {
	case strings.HasPrefix(val, "/"):
		if !stream {
			return "", fmt.Errorf("slinit opens no Unix datagram sockets")
		}
		return val, nil
	case strings.HasPrefix(val, "@"):
		return "", fmt.Errorf("slinit opens no abstract Unix sockets")
	case strings.HasPrefix(val, "vsock:"):
		return "", fmt.Errorf("slinit opens no AF_VSOCK sockets")
	case strings.Contains(val, "%"):
		return "", fmt.Errorf("slinit cannot bind a socket to an interface")
	}
	proto := "udp:"
	if stream {
		proto = "tcp:"
	}
	if p, err := strconv.Atoi(val); err == nil {
		if p <= 0 || p > 65535 {
			return "", fmt.Errorf("port %d out of range", p)
		}
		return proto + "[::]:" + val, nil
	}
	i := strings.LastIndex(val, ":")
	if i <= 0 {
		return "", fmt.Errorf("not a path, port or host:port")
	}
	if p, err := strconv.Atoi(val[i+1:]); err != nil || p <= 0 || p > 65535 {
		return "", fmt.Errorf("not a path, port or host:port")
	}
	return proto + val, nil
}

// sdLookupID resolves SocketUser= / SocketGroup= to the number slinit's
// socket-uid / socket-gid take.
func sdLookupID(key, v string) (string, error) {
	if _, err := strconv.Atoi(v); err == nil {
		return v, nil
	}
	if key == "SocketUser" {
		u, err := user.Lookup(v)
		if err != nil {
			return "", err
		}
		return u.Uid, nil
	}
	g, err := user.LookupGroup(v)
	if err != nil {
		return "", err
	}
	return g.Gid, nil
}
