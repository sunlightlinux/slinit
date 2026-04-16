//go:build linux && cgo

// Package utmp provides utmpx database functions for logging boot time
// and service process entries to /var/run/utmp and /var/log/wtmp.
// This mirrors dinit's USE_UTMPX functionality.
package utmp

/*
#include <stdlib.h>
#include <utmpx.h>
#include <utmp.h>
#include <string.h>
#include <sys/time.h>
#include <unistd.h>
#include <paths.h>

// Fallback paths
#ifndef _PATH_UTMPX
#define _PATH_UTMPX "/var/run/utmp"
#endif
#ifndef _PATH_WTMP
#define _PATH_WTMP "/var/log/wtmp"
#endif

// c_log_boot clears utmp, writes a BOOT_TIME record to utmp and wtmp.
static int c_log_boot(void) {
    struct utmpx record;
    memset(&record, 0, sizeof(record));
    record.ut_type = BOOT_TIME;

    struct timeval tv;
    gettimeofday(&tv, NULL);
    record.ut_tv.tv_sec = tv.tv_sec;
    record.ut_tv.tv_usec = tv.tv_usec;

    // Clear utmp on boot (same as dinit's CLEAR_UTMP_ON_BOOT)
    if (truncate(_PATH_UTMPX, 0) < 0) {
        // Best-effort: ignore failure (file may not exist yet)
    }

    // Append to wtmp using utmp struct (Linux-compatible)
    struct utmp wrecord;
    memset(&wrecord, 0, sizeof(wrecord));
    wrecord.ut_type = BOOT_TIME;
    wrecord.ut_tv.tv_sec = tv.tv_sec;
    wrecord.ut_tv.tv_usec = tv.tv_usec;
    updwtmp(_PATH_WTMP, &wrecord);

    // Write to utmp
    setutxent();
    pututxline(&record);
    endutxent();

    return 1;
}

// c_create_entry writes an INIT_PROCESS record to utmp.
static int c_create_entry(const char *id, const char *line, int pid) {
    struct utmpx record;
    memset(&record, 0, sizeof(record));
    record.ut_type = INIT_PROCESS;
    record.ut_pid = pid;

    strncpy(record.ut_id, id, sizeof(record.ut_id));
    strncpy(record.ut_line, line, sizeof(record.ut_line));

    struct timeval tv;
    gettimeofday(&tv, NULL);
    record.ut_tv.tv_sec = tv.tv_sec;
    record.ut_tv.tv_usec = tv.tv_usec;

    setutxent();
    pututxline(&record);
    endutxent();

    return 1;
}

// c_clear_entry writes a DEAD_PROCESS record to utmp,
// looking up the existing entry to preserve the PID field.
static void c_clear_entry(const char *id, const char *line) {
    struct utmpx record;
    memset(&record, 0, sizeof(record));
    record.ut_type = DEAD_PROCESS;

    strncpy(record.ut_id, id, sizeof(record.ut_id));
    strncpy(record.ut_line, line, sizeof(record.ut_line));

    struct timeval tv;
    gettimeofday(&tv, NULL);
    record.ut_tv.tv_sec = tv.tv_sec;
    record.ut_tv.tv_usec = tv.tv_usec;

    // Try to find existing entry to copy PID
    setutxent();
    struct utmpx *existing = NULL;
    if (id[0] != '\0') {
        existing = getutxid(&record);
    } else if (line[0] != '\0') {
        existing = getutxline(&record);
    }
    if (existing != NULL) {
        record.ut_pid = existing->ut_pid;
    }

    // Reset type after getutxid may have changed it
    record.ut_type = DEAD_PROCESS;
    pututxline(&record);
    endutxent();
}

// c_max_id_len returns sizeof(utmpx.ut_id).
static int c_max_id_len(void) {
    return sizeof(((struct utmpx *)0)->ut_id);
}

// c_max_line_len returns sizeof(utmpx.ut_line).
static int c_max_line_len(void) {
    return sizeof(((struct utmpx *)0)->ut_line);
}

// c_next_user_line iterates utmpx looking for USER_PROCESS entries.
// Call with reset=1 to restart the iteration, reset=0 to continue.
// Writes the ut_line value into out (caller-provided buffer of at least
// UT_LINESIZE+1 bytes). Returns 1 if an entry was written, 0 on end.
static int c_next_user_line(char *out, int reset) {
    if (reset) {
        setutxent();
    }
    struct utmpx *ent;
    while ((ent = getutxent()) != NULL) {
        if (ent->ut_type != USER_PROCESS) {
            continue;
        }
        if (ent->ut_line[0] == '\0') {
            continue;
        }
        // Copy up to sizeof(ut_line), ensuring NUL termination.
        size_t n = sizeof(ent->ut_line);
        memcpy(out, ent->ut_line, n);
        out[n] = '\0';
        return 1;
    }
    endutxent();
    return 0;
}

// c_next_user_session is like c_next_user_line but also writes the
// ut_user field into outUser (caller buffer of at least UT_NAMESIZE+1).
// Used by shutdown.allow access control to check whether any authorised
// user is currently logged in.
static int c_next_user_session(char *outUser, char *outLine, int reset) {
    if (reset) {
        setutxent();
    }
    struct utmpx *ent;
    while ((ent = getutxent()) != NULL) {
        if (ent->ut_type != USER_PROCESS) {
            continue;
        }
        if (ent->ut_user[0] == '\0') {
            continue;
        }
        size_t un = sizeof(ent->ut_user);
        memcpy(outUser, ent->ut_user, un);
        outUser[un] = '\0';
        size_t ln = sizeof(ent->ut_line);
        memcpy(outLine, ent->ut_line, ln);
        outLine[ln] = '\0';
        return 1;
    }
    endutxent();
    return 0;
}

// c_max_user_len returns sizeof(utmpx.ut_user).
static int c_max_user_len(void) {
    return sizeof(((struct utmpx *)0)->ut_user);
}

// c_logout_all_users rewrites every USER_PROCESS entry in utmp as
// DEAD_PROCESS and appends a matching logout record to wtmp so last(1)
// can display a clean session boundary across a shutdown. The utmp
// rewrite preserves ut_id and ut_line so getutxid/getutxline matches;
// the wtmp record has a blank ut_user per sysvinit convention.
// Returns the number of sessions logged out.
static int c_logout_all_users(void) {
    struct timeval tv;
    gettimeofday(&tv, NULL);

    // Snapshot USER_PROCESS entries first so we don't mutate the file
    // while iterating. A cap of 256 sessions is well above anything a
    // real init will see (utmp is for login sessions, not connections).
    struct utmpx saved[256];
    int n = 0;

    setutxent();
    struct utmpx *ent;
    while (n < (int)(sizeof(saved)/sizeof(saved[0])) && (ent = getutxent()) != NULL) {
        if (ent->ut_type != USER_PROCESS) continue;
        if (ent->ut_line[0] == '\0') continue;
        saved[n++] = *ent;
    }
    endutxent();

    for (int i = 0; i < n; i++) {
        struct utmpx dead;
        memset(&dead, 0, sizeof(dead));
        dead.ut_type = DEAD_PROCESS;
        memcpy(dead.ut_id, saved[i].ut_id, sizeof(dead.ut_id));
        memcpy(dead.ut_line, saved[i].ut_line, sizeof(dead.ut_line));
        dead.ut_pid = saved[i].ut_pid;
        dead.ut_tv.tv_sec = tv.tv_sec;
        dead.ut_tv.tv_usec = tv.tv_usec;

        setutxent();
        pututxline(&dead);
        endutxent();

        struct utmp wrec;
        memset(&wrec, 0, sizeof(wrec));
        wrec.ut_type = DEAD_PROCESS;
        memcpy(wrec.ut_id, saved[i].ut_id, sizeof(wrec.ut_id));
        memcpy(wrec.ut_line, saved[i].ut_line, sizeof(wrec.ut_line));
        wrec.ut_pid = saved[i].ut_pid;
        wrec.ut_tv.tv_sec = tv.tv_sec;
        wrec.ut_tv.tv_usec = tv.tv_usec;
        updwtmp(_PATH_WTMP, &wrec);
    }
    return n;
}

// c_log_shutdown appends a RUN_LVL "shutdown" record to wtmp so that
// `last -x` renders a system-shutdown boundary. Matches sysvinit's
// convention of ut_user="shutdown", ut_line="~", ut_id="~~".
static int c_log_shutdown(void) {
    struct utmp rec;
    memset(&rec, 0, sizeof(rec));
    rec.ut_type = RUN_LVL;
    strncpy(rec.ut_user, "shutdown", sizeof(rec.ut_user));
    strncpy(rec.ut_line, "~", sizeof(rec.ut_line));
    strncpy(rec.ut_id, "~~", sizeof(rec.ut_id));

    struct timeval tv;
    gettimeofday(&tv, NULL);
    rec.ut_tv.tv_sec = tv.tv_sec;
    rec.ut_tv.tv_usec = tv.tv_usec;

    updwtmp(_PATH_WTMP, &rec);
    return 1;
}
*/
import "C"

import "unsafe"

// MaxIDLen is the maximum length of an inittab-id value.
var MaxIDLen = int(C.c_max_id_len())

// MaxLineLen is the maximum length of an inittab-line value.
var MaxLineLen = int(C.c_max_line_len())

// LogBoot writes a BOOT_TIME record to utmp and wtmp.
// It clears the utmp file first (same as dinit's CLEAR_UTMP_ON_BOOT).
// Should be called once when the root filesystem becomes read-write.
func LogBoot() bool {
	return C.c_log_boot() != 0
}

// CreateEntry writes an INIT_PROCESS record to utmp for a started service.
// id and line correspond to the service's inittab-id and inittab-line settings.
// pid is the process ID of the started service.
func CreateEntry(id, line string, pid int) bool {
	cID := C.CString(id)
	cLine := C.CString(line)
	defer C.free(unsafe.Pointer(cID))
	defer C.free(unsafe.Pointer(cLine))
	return C.c_create_entry(cID, cLine, C.int(pid)) != 0
}

// ClearEntry writes a DEAD_PROCESS record to utmp for a stopped service.
// It looks up the existing entry by id or line to preserve the PID field.
func ClearEntry(id, line string) {
	cID := C.CString(id)
	cLine := C.CString(line)
	defer C.free(unsafe.Pointer(cID))
	defer C.free(unsafe.Pointer(cLine))
	C.c_clear_entry(cID, cLine)
}

// ListUserTTYs returns the ut_line values (TTY device names, e.g. "tty1",
// "pts/0") of all active USER_PROCESS entries in the utmp database.
// Used by the shutdown wall broadcast to find logged-in users.
func ListUserTTYs() []string {
	buf := make([]byte, MaxLineLen+1)
	cBuf := (*C.char)(unsafe.Pointer(&buf[0]))

	var lines []string
	reset := C.int(1)
	for C.c_next_user_line(cBuf, reset) != 0 {
		reset = 0
		n := 0
		for n < len(buf) && buf[n] != 0 {
			n++
		}
		if n > 0 {
			lines = append(lines, string(buf[:n]))
		}
	}
	return lines
}

// MaxUserLen is the maximum length of an ut_user value.
var MaxUserLen = int(C.c_max_user_len())

// Session represents a single logged-in user session from the utmpx
// database: the user name and the TTY/pseudoterminal device they are
// attached to.
type Session struct {
	User string
	Line string
}

// ListUserSessions returns one Session per active USER_PROCESS entry in
// the utmp database. Used by shutdown.allow access control to verify
// that at least one authorised user is currently logged in before
// honouring a signal-driven shutdown.
func ListUserSessions() []Session {
	userBuf := make([]byte, MaxUserLen+1)
	lineBuf := make([]byte, MaxLineLen+1)
	cUser := (*C.char)(unsafe.Pointer(&userBuf[0]))
	cLine := (*C.char)(unsafe.Pointer(&lineBuf[0]))

	var sessions []Session
	reset := C.int(1)
	for C.c_next_user_session(cUser, cLine, reset) != 0 {
		reset = 0
		sessions = append(sessions, Session{
			User: cStringUpTo(userBuf),
			Line: cStringUpTo(lineBuf),
		})
	}
	return sessions
}

// cStringUpTo returns the NUL-terminated prefix of buf as a Go string.
func cStringUpTo(buf []byte) string {
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n])
}

// LogoutAllUsers marks every active USER_PROCESS entry in utmp as
// DEAD_PROCESS and appends a matching logout record to wtmp. This is
// the shutdown counterpart of LogBoot: after it runs, `last` shows a
// clean session boundary instead of still-logged-in sessions bleeding
// across the reboot. Returns the number of sessions logged out.
func LogoutAllUsers() int {
	return int(C.c_logout_all_users())
}

// LogShutdown appends a RUN_LVL "shutdown" record to wtmp so that
// `last -x` renders a system-shutdown boundary. Called from the
// shutdown sequence after LogoutAllUsers and before processes are
// killed, matching sysvinit behaviour.
func LogShutdown() bool {
	return C.c_log_shutdown() != 0
}
