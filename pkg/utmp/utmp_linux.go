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
    truncate(_PATH_UTMPX, 0);

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
