package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/dinitwire"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// peerIsDinit is set by versionHandshake when the daemon on the other end of
// the socket is dinit, not slinit. The two share command numbers but not a
// wire format, so the connection is then wrapped in a dinitConn; the few
// places that must behave differently for dinit consult this.
var peerIsDinit bool

// dinitUnsupported is the error for a command dinit has no counterpart for.
type dinitUnsupported struct {
	what string
}

func (e *dinitUnsupported) Error() string {
	return e.what + " is not supported by a dinit daemon (it is a slinit extension)"
}

// dinitCmdNames names slinit's extension commands for dinitUnsupported.
// A command missing from here is reported by number.
var dinitCmdNames = map[uint8]string{
	control.CmdBootTime:            "boot-time",
	control.CmdDisableService:      "disable",
	control.CmdDisableServiceV7:    "disable",
	control.CmdQueryDependents:     "dependents",
	control.CmdPauseService:        "pause",
	control.CmdContinueService:     "continue",
	control.CmdOnceService:         "once",
	control.CmdQueryDependencies:   "the dependency graph",
	control.CmdQueryDescription:    "the service description text",
	control.CmdRunAction:           "action",
	control.CmdListActions:         "list-actions",
	control.CmdScheduleShutdown:    "a scheduled or fast shutdown",
	control.CmdCancelShutdown:      "shutdown -c",
	control.CmdQueryShutdown:       "shutdown --status",
	control.CmdReloadAll:           "reload-all",
	control.CmdReloadSignal:        "reload-signal",
	control.CmdResetEnv:            "resetenv",
	control.CmdQueryMetadata:       "service metadata",
	control.CmdActivateProfile:     "profile activation",
	control.CmdQueryProfile:        "profiles",
	control.CmdListProfiles:        "profiles",
	control.CmdQueryBundleMembers:  "bundles",
	control.CmdWallNotice:          "a wall notice",
	control.CmdResetFailed:         "reset-failed",
	control.CmdFreezeService:       "freeze",
	control.CmdThawService:         "thaw",
	control.CmdJournalQuery:        "the journal",
	control.CmdJournalSubscribe:    "the journal",
	control.CmdQueryServiceLoadDir: "the per-service load directory",
	control.CmdSwitchRoot:          "switch-root",
	control.CmdSuspend:             "suspend",
	control.CmdServiceShow:         "show",
	control.CmdStartAll:            "start-all",
	control.CmdServiceRusage:       "rusage",
	control.CmdCleanService:        "clean",
}

// dinitConn makes a dinit daemon look like a slinit one to the rest of
// slinitctl. Writes are slinit-framed requests: each is translated into
// dinit's layout, exchanged with the daemon at once, and the reply is
// queued in slinit's framing for the next Read. With nothing queued, Read
// waits for dinit's next information packet and hands that over, also in
// slinit's framing.
type dinitConn struct {
	net.Conn // the socket: deadlines and Close pass straight through
	d        *dinitwire.Conn
	wbuf     []byte // slinit request bytes not yet a whole packet
	rbuf     []byte // slinit-framed replies waiting to be read
	minVer   uint16
	ver      uint16
}

func newDinitConn(nc net.Conn, d *dinitwire.Conn, minVer, ver uint16) *dinitConn {
	return &dinitConn{Conn: nc, d: d, minVer: minVer, ver: ver}
}

func (c *dinitConn) queue(typ uint8, payload []byte) {
	hdr := [3]byte{typ}
	binary.LittleEndian.PutUint16(hdr[1:], uint16(len(payload)))
	c.rbuf = append(c.rbuf, hdr[:]...)
	c.rbuf = append(c.rbuf, payload...)
}

// queueEvents moves the information packets dinit sent ahead of a reply into
// the read queue, so the caller sees them in the order the daemon wrote them.
func (c *dinitConn) queueEvents() {
	for c.d.PendingEvents() > 0 {
		ev, err := c.d.NextEvent()
		if err != nil {
			return
		}
		c.queueEvent(ev)
	}
}

func (c *dinitConn) queueEvent(ev dinitwire.Event) {
	switch ev.Type {
	case dinitwire.InfoServiceEvent5:
		buf := make([]byte, 5, 19)
		binary.LittleEndian.PutUint32(buf, ev.Handle)
		buf[4] = ev.Event
		c.queue(control.InfoServiceEvent5, append(buf, dinitStatus5(ev.Status)...))
	case dinitwire.InfoServiceEvent:
		buf := make([]byte, 5, 17)
		binary.LittleEndian.PutUint32(buf, ev.Handle)
		buf[4] = ev.Event
		c.queue(control.InfoServiceEvent, append(buf, dinitStatus1(ev.Status)...))
	case dinitwire.InfoEnvEvent:
		c.queue(control.InfoEnvEvent, control.EncodeEnvEvent(ev.Var, ev.Overridden))
	}
}

func (c *dinitConn) Read(p []byte) (int, error) {
	if len(c.rbuf) == 0 {
		ev, err := c.d.NextEvent()
		if err != nil {
			return 0, err
		}
		c.queueEvent(ev)
	}
	n := copy(p, c.rbuf)
	c.rbuf = c.rbuf[n:]
	return n, nil
}

func (c *dinitConn) Write(p []byte) (int, error) {
	c.wbuf = append(c.wbuf, p...)
	for len(c.wbuf) >= 3 {
		plen := int(binary.LittleEndian.Uint16(c.wbuf[1:]))
		if len(c.wbuf) < 3+plen {
			break
		}
		cmd, payload := c.wbuf[0], c.wbuf[3:3+plen]
		err := c.exchange(cmd, payload)
		c.wbuf = c.wbuf[3+plen:]
		if err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// dinitFlags turns dinit's status flag bits into slinit's. Waiting-for-
// console and start-skipped have no bit on slinit's side.
func dinitFlags(st dinitwire.Status) uint8 {
	var f uint8
	if st.HasPID {
		f |= control.StatusFlagHasPID
	}
	if st.Flags&dinitwire.FlagMarkedActive != 0 {
		f |= control.StatusFlagMarkedActive
	}
	if st.Flags&dinitwire.FlagHasConsole != 0 {
		f |= control.StatusFlagHasConsole
	}
	if !st.HasPID && service.StoppedReason(st.StopReason) == service.ReasonExecFailed {
		f |= control.StatusFlagExecFailed
	}
	return f
}

// dinitStatus5 lays a dinit status out as slinit's 14-byte v5 status. dinit
// puts the pid where the wait status goes while the process runs; slinit's
// v5 has no pid, so those fields are zero then.
func dinitStatus5(st dinitwire.Status) []byte {
	b := make([]byte, 14)
	b[0], b[1], b[2], b[3] = st.State, st.Target, dinitFlags(st), st.StopReason
	binary.LittleEndian.PutUint16(b[4:], st.ExecStage)
	if !st.HasPID {
		binary.LittleEndian.PutUint32(b[6:], uint32(st.SiCode))
		binary.LittleEndian.PutUint32(b[10:], uint32(st.SiStatus))
	}
	return b
}

// dinitStatus6 is dinitStatus5 plus the description's modification time in
// seconds, slinit's 22-byte v6 status.
func dinitStatus6(st dinitwire.Status) []byte {
	b := append(dinitStatus5(st), make([]byte, 8)...)
	binary.LittleEndian.PutUint64(b[14:], uint64(st.ModSec))
	return b
}

// cldExited is CLD_EXITED: the process exited rather than being signalled.
const cldExited = 1

// dinitStatus1 lays a dinit status out as slinit's 12-byte v1 status. dinit
// never says what type a service is, so the type is the placeholder; the
// exit status comes from the v5 wait status when there is one (dinit's own
// v1 only has 0 or -1), else from dinit's v1 field.
func dinitStatus1(st dinitwire.Status) []byte {
	b := make([]byte, 12)
	b[0], b[1], b[2], b[3] = st.State, st.Target, uint8(service.TypePlaceholder), dinitFlags(st)
	if st.HasPID {
		binary.LittleEndian.PutUint32(b[4:], uint32(st.PID))
		return b
	}
	exit := st.ExitStatus
	if st.SiCode == cldExited {
		exit = st.SiStatus
	} else if st.SiCode != 0 {
		exit = -1
	}
	binary.LittleEndian.PutUint32(b[8:], uint32(exit))
	return b
}

func (c *dinitConn) badReq() { c.queue(control.RplyBadReq, nil) }

// exchange performs one slinit request against dinit and queues the reply.
func (c *dinitConn) exchange(cmd uint8, p []byte) error {
	d := c.d
	defer c.queueEvents()

	switch cmd {
	case control.CmdQueryVersion:
		b := make([]byte, 4)
		binary.LittleEndian.PutUint16(b, c.minVer)
		binary.LittleEndian.PutUint16(b[2:], c.ver)
		c.queue(control.RplyCPVersion, b)

	case control.CmdFindService, control.CmdLoadService:
		name, _, err := control.DecodeServiceName(p)
		if err != nil {
			c.badReq()
			return nil
		}
		code, rec, err := d.FindOrLoad(cmd, name)
		if err != nil {
			return err
		}
		c.queueEvents()
		if code != dinitwire.RplyServiceRecord {
			c.queue(code, nil)
			return nil
		}
		b := make([]byte, 6)
		b[0] = rec.State
		binary.LittleEndian.PutUint32(b[1:], rec.Handle)
		b[5] = rec.Target
		c.queue(control.RplyServiceRecord, b)

	case control.CmdStartService, control.CmdWakeService, control.CmdReleaseService, control.CmdStopService:
		if len(p) < 4 {
			c.badReq()
			return nil
		}
		handle := binary.LittleEndian.Uint32(p)
		var sf uint8
		if len(p) >= 5 {
			sf = p[4]
		}
		// Pin and pre-ack are the same bits on both sides. For a stop,
		// slinit's bit 1 means "force" where dinit's means "gentle", the
		// opposite; restart is bit 2 on both.
		df := sf & (dinitwire.StartStopPin | dinitwire.StartStopPreACK)
		if cmd == control.CmdStopService {
			if sf&0x02 == 0 {
				df |= dinitwire.StartStopGentle
			}
			df |= sf & dinitwire.StartStopRestart
		}
		rep, err := d.StartStop(cmd, df, handle)
		if err != nil {
			return err
		}
		if rep.Code == dinitwire.RplyDependents {
			return c.dependentsError(handle, rep.Dependents)
		}
		c.queueEvents()
		if rep.PreACK {
			c.queue(control.RplyPreACK, nil)
		}
		c.queue(rep.Code, nil)

	case control.CmdUnpinService, control.CmdReloadService, control.CmdUnloadService, control.CmdCloseHandle:
		if len(p) < 4 {
			c.badReq()
			return nil
		}
		code, err := d.HandleCmd(cmd, binary.LittleEndian.Uint32(p))
		if err != nil {
			return err
		}
		c.queueEvents()
		c.queue(code, nil)

	case control.CmdShutdown:
		if len(p) < 1 {
			c.badReq()
			return nil
		}
		if len(p) >= 2 && p[1] != 0 {
			return &dinitUnsupported{"a shutdown with slinit's flags (now, --fast, --superfast)"}
		}
		code, err := d.Shutdown(p[0])
		if err != nil {
			return err
		}
		c.queue(code, nil)

	case control.CmdListServices, control.CmdListServices5:
		list, err := d.List()
		if err != nil {
			return err
		}
		c.queueEvents()
		for _, e := range list {
			b := control.EncodeServiceName(e.Name)
			if cmd == control.CmdListServices5 {
				b = append(b, dinitStatus5(e.Status)...)
			} else {
				// slinit's v1 entry is the v1 status without its exit field.
				b = append(b, dinitStatus1(e.Status)[:8]...)
			}
			c.queue(control.RplySvcInfo, b)
		}
		c.queue(control.RplyListDone, nil)

	case control.CmdServiceStatus, control.CmdServiceStatus5, control.CmdServiceStatus6:
		if len(p) < 4 {
			c.badReq()
			return nil
		}
		// Always ask dinit for its v6: it is a superset, and its wait
		// status gives slinit's v1 a real exit code where dinit's v1
		// would give only 0 or -1.
		code, st, err := d.Status(dinitwire.CmdServiceStatus6, binary.LittleEndian.Uint32(p))
		if err != nil {
			return err
		}
		c.queueEvents()
		if code != dinitwire.RplyServiceStatus {
			c.queue(code, nil)
			return nil
		}
		switch cmd {
		case control.CmdServiceStatus:
			c.queue(control.RplyServiceStatus, dinitStatus1(st))
		case control.CmdServiceStatus5:
			c.queue(control.RplyServiceStatus, dinitStatus5(st))
		default:
			c.queue(control.RplyServiceStatus, dinitStatus6(st))
		}

	case control.CmdAddDep, control.CmdRmDep, control.CmdRmDepV7, control.CmdEnableService, control.CmdEnableServiceV7:
		from, to, depType, err := control.DecodeDepRequest(p)
		if err != nil {
			c.badReq()
			return nil
		}
		rep, err := d.Dep(cmd, depType, from, to)
		if err != nil {
			return err
		}
		c.queueEvents()
		if rep.Code != dinitwire.RplyServiceStatus {
			c.queue(rep.Code, nil)
			return nil
		}
		b := []byte{0}
		if rep.Flag {
			b[0] = 1
		}
		c.queue(control.RplyServiceStatus, append(b, dinitStatus6(rep.Status)...))

	case control.CmdQueryServiceName:
		if len(p) < 4 {
			c.badReq()
			return nil
		}
		code, name, err := d.QueryServiceName(binary.LittleEndian.Uint32(p))
		if err != nil {
			return err
		}
		if code != dinitwire.RplyServiceName {
			c.queue(code, nil)
			return nil
		}
		// slinit answers this with its own reply number.
		c.queue(control.RplyServiceName, control.EncodeServiceName(name))

	case control.CmdQueryLoadMech, control.CmdQueryServiceDscDir:
		code, lm, err := d.QueryLoadMech()
		if err != nil {
			return err
		}
		if code != dinitwire.RplyLoaderMech {
			c.queue(code, nil)
			return nil
		}
		if cmd == control.CmdQueryLoadMech {
			b := []byte{lm.Type}
			b = binary.LittleEndian.AppendUint32(b, uint32(len(lm.Cwd)))
			b = append(b, lm.Cwd...)
			b = binary.LittleEndian.AppendUint32(b, uint32(len(lm.Dirs)))
			for _, dir := range lm.Dirs {
				b = binary.LittleEndian.AppendUint32(b, uint32(len(dir)))
				b = append(b, dir...)
			}
			c.queue(control.RplyLoaderMech, b)
			return nil
		}
		// slinit's description-dir query takes no handle and lists every
		// service directory, absolute; dinit's is per service. The load
		// mechanism carries the same list, relative to the daemon's cwd.
		b := binary.LittleEndian.AppendUint16(nil, uint16(len(lm.Dirs)))
		for _, dir := range lm.Dirs {
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(lm.Cwd, dir)
			}
			b = binary.LittleEndian.AppendUint16(b, uint16(len(dir)))
			b = append(b, dir...)
		}
		c.queue(control.RplyServiceDscDir, b)

	case control.CmdSetEnv:
		handle, key, value, unset, err := control.DecodeSetEnv(p)
		if err != nil {
			c.badReq()
			return nil
		}
		if handle != 0 {
			return &dinitUnsupported{"a per-service environment (dinit's is global: use setenv-global)"}
		}
		v := key
		if !unset {
			v = key + "=" + value
		}
		code, err := d.SetEnv(v)
		if err != nil {
			return err
		}
		c.queue(code, nil)

	case control.CmdGetAllEnv:
		if len(p) >= 4 && binary.LittleEndian.Uint32(p) != 0 {
			return &dinitUnsupported{"a per-service environment (dinit's is global: use getallenv-global)"}
		}
		code, env, err := d.GetAllEnv()
		if err != nil {
			return err
		}
		if code != dinitwire.RplyAllEnv {
			c.queue(code, nil)
			return nil
		}
		m := make(map[string]string, len(env))
		for _, e := range env {
			if k, v, ok := strings.Cut(e, "="); ok {
				m[k] = v
			}
		}
		c.queue(control.RplyEnvList, control.EncodeEnvList(m))

	case control.CmdListenEnv:
		code, err := d.ListenEnv()
		if err != nil {
			return err
		}
		c.queue(code, nil)

	case control.CmdSetTrigger:
		if len(p) < 5 {
			c.badReq()
			return nil
		}
		code, err := d.SetTrigger(binary.LittleEndian.Uint32(p), p[4] != 0)
		if err != nil {
			return err
		}
		c.queueEvents()
		c.queue(code, nil)

	case control.CmdSignal:
		if len(p) < 8 {
			c.badReq()
			return nil
		}
		code, err := d.Signal(int32(binary.LittleEndian.Uint32(p[4:])), binary.LittleEndian.Uint32(p))
		if err != nil {
			return err
		}
		c.queue(code, nil)

	case control.CmdCatLog:
		flags, handle, err := control.DecodeCatLogRequest(p)
		if err != nil {
			c.badReq()
			return nil
		}
		code, data, err := d.CatLog(flags, handle)
		if err != nil {
			return err
		}
		if code != dinitwire.RplyServiceLog {
			c.queue(code, nil)
			return nil
		}
		c.queue(control.RplySvcLog, control.EncodeSvcLog(data))

	default:
		if name, ok := dinitCmdNames[cmd]; ok {
			return &dinitUnsupported{name}
		}
		return &dinitUnsupported{fmt.Sprintf("command %d", cmd)}
	}
	return nil
}

// enableDisableDinit does enable or disable against a dinit daemon the way
// dinitctl does: the runtime waits-for dependency over the control socket,
// and the persistent waits-for.d symlink on disk. slinit's own enable and
// disable are single opcodes a dinit daemon has no counterpart for, so
// slinitctl routes here instead when the peer is dinit.
//
// The symlink side reads the service descriptions from disk, as slinitctl's
// offline enable does: a control-socket client shares a machine with its
// daemon, so the files are right there.
func enableDisableDinit(conn net.Conn, enable bool, name, from string, noWait bool) error {
	_ = noWait // dinit has no "wait for enabled to start" round trip to skip

	dirs, err := queryServiceDirs(conn)
	if err != nil {
		return fmt.Errorf("enable/disable against dinit: cannot find the service directory: %w", err)
	}
	// Pick the directory that actually holds the target's description, so
	// offlineLinkPath reads the right files when there is more than one.
	svcDir := ""
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, stripServiceArg(name))); err == nil {
			svcDir = d
			break
		}
	}
	if svcDir == "" && len(dirs) > 0 {
		svcDir = dirs[0]
	}

	// offlineLinkPath resolves an empty "from" to the target's enable-via
	// or to boot, the same resolution the symlink step below uses; take the
	// resolved name so the runtime dependency and the symlink agree.
	_, resolvedFrom, err := offlineLinkPath(svcDir, from, name, true)
	if err != nil {
		return err
	}
	fromHandle, err := loadServiceHandle(conn, resolvedFrom)
	if err != nil {
		return err
	}
	toHandle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	// The runtime half: enable adds the waits-for dependency and starts the
	// target (ENABLE_SERVICE_V7); disable removes it (REM_DEP_V7). The
	// adapter turns these into dinit's own layouts. REM_DEP is used on a
	// pre-v7 daemon, but every dinit that reaches here is at version 7.
	cmd := control.CmdEnableServiceV7
	if !enable {
		cmd = control.CmdRmDepV7
		if peerCPVersion < 7 {
			cmd = control.CmdRmDep
		}
	} else if peerCPVersion < 7 {
		cmd = control.CmdEnableService
	}
	payload := control.EncodeDepRequest(fromHandle, toHandle, uint8(service.DepWaitsFor))
	if err := control.WritePacket(conn, cmd, payload); err != nil {
		return err
	}
	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	switch rply {
	case control.RplyACK, control.RplyServiceStatus:
		// Done; the symlink follows.
	case control.RplyNAK:
		if enable {
			return fmt.Errorf("could not enable '%s': the daemon refused the dependency (a cycle?)", name)
		}
		// disable: the dependency was not there at runtime. The on-disk
		// symlink may still be, so fall through and remove it.
	default:
		return fmt.Errorf("enable/disable against dinit: unexpected reply %d", rply)
	}

	// The persistent half: the symlink, with its own message.
	if enable {
		return offlineEnable(svcDir, from, name)
	}
	return offlineDisable(svcDir, from, name)
}

// dependentsError reports a stop dinit refused because other services
// depend on the target, naming them the way dinitctl does.
func (c *dinitConn) dependentsError(handle uint32, dependents []uint32) error {
	var names []string
	for _, h := range dependents {
		if code, name, err := c.d.QueryServiceName(h); err == nil && code == dinitwire.RplyServiceName {
			names = append(names, name)
		}
	}
	return fmt.Errorf("cannot stop the service: these depend on it and would stop too: %s (use --force to stop them as well)",
		strings.Join(names, ", "))
}
