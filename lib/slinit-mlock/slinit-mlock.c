/*
 * slinit-mlock — LD_PRELOAD helper behind the `mlockall` service setting.
 *
 * Memory locks do not survive execve(2), and no syscall locks another
 * process's memory, so slinit-runner cannot lock the service's pages
 * itself: the mlockall(2) call has to happen inside the service. The
 * runner preloads this library and passes the flags in SLINIT_MLOCKALL;
 * the constructor below runs before main() and makes the call.
 *
 * A failed mlockall is fatal (exit 127): the configuration asked for
 * locked memory, and a real-time service quietly running unlocked is the
 * failure the setting exists to prevent.
 *
 * The lock belongs to the service's main process, whatever it execs
 * into: `sh -c '...; exec daemon'` must end with the daemon locked, and
 * locks do not survive that exec either. So the runner also passes its
 * PID — which the service keeps across exec — in SLINIT_MLOCKALL_PID.
 * In that process the variables stay set and every exec locks again. In
 * any other process (a child the service forked) the library clears
 * them and removes itself from LD_PRELOAD, so the service's own children
 * and everything they run are left alone.
 *
 * Only plain libc calls are used — no stdio, no strtol, nothing FORTIFY
 * rewrites — so one build loads under glibc of any age and under musl,
 * whose loader resolves a glibc DT_NEEDED on libc.so.6 to itself.
 *
 * Build: see Makefile.
 */
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

#define SELF "libslinit-mlock.so"

static void say(const char *s)
{
	(void)!write(2, s, strlen(s));
}

static void die(const char *what, const char *detail)
{
	say("slinit-mlock: ");
	say(what);
	say(detail);
	say("\n");
	_exit(127);
}

/* Drop every LD_PRELOAD entry naming this library. Entries are
 * separated by ':' or ' ', as ld.so accepts either. */
static void strip_self_from_preload(void)
{
	const char *cur = getenv("LD_PRELOAD");
	if (cur == NULL)
		return;

	size_t n = strlen(cur);
	char *out = malloc(n + 1);
	if (out == NULL)
		return;
	size_t o = 0;

	const char *p = cur;
	while (*p != '\0') {
		while (*p == ':' || *p == ' ')
			p++;
		const char *start = p;
		while (*p != '\0' && *p != ':' && *p != ' ')
			p++;
		size_t len = (size_t)(p - start);
		if (len == 0)
			continue;

		const char *base = start;
		for (const char *q = start; q < p; q++)
			if (*q == '/')
				base = q + 1;
		size_t blen = (size_t)(p - base);
		if (blen == sizeof(SELF) - 1 && memcmp(base, SELF, blen) == 0)
			continue;

		if (o > 0)
			out[o++] = ':';
		memcpy(out + o, start, len);
		o += len;
	}
	out[o] = '\0';

	if (o == 0)
		unsetenv("LD_PRELOAD");
	else
		setenv("LD_PRELOAD", out, 1);
	free(out);
}

/* Parse a positive decimal number; -1 if s is not one. */
static long parse_pos(const char *s)
{
	long n = 0;
	const char *p = s;
	for (; *p >= '0' && *p <= '9'; p++) {
		n = n * 10 + (*p - '0');
		if (n > 0x7fffffff)
			return -1;
	}
	if (p == s || *p != '\0' || n <= 0)
		return -1;
	return n;
}

__attribute__((constructor)) static void slinit_mlock(void)
{
	const char *v = getenv("SLINIT_MLOCKALL");
	if (v == NULL)
		return;

	const char *pv = getenv("SLINIT_MLOCKALL_PID");
	if (pv == NULL || parse_pos(pv) != (long)getpid()) {
		/* A process the service forked: not ours to lock. */
		unsetenv("SLINIT_MLOCKALL");
		unsetenv("SLINIT_MLOCKALL_PID");
		strip_self_from_preload();
		return;
	}

	long flags = parse_pos(v);
	if (flags < 0)
		die("bad SLINIT_MLOCKALL=", v);
	if (mlockall((int)flags) != 0)
		die("mlockall: ", strerror(errno));
}
