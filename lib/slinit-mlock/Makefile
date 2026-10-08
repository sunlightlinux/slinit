# libslinit-mlock.so — preloaded by slinit-runner for the `mlockall`
# service setting (see slinit-mlock.c). Install it where the runner looks:
# <prefix>/lib/slinit/, next to <prefix>/sbin/slinit-runner.

CC      ?= cc
CFLAGS  ?= -O2
PREFIX  ?= /usr
LIBDIR  ?= $(PREFIX)/lib/slinit

all: libslinit-mlock.so

libslinit-mlock.so: slinit-mlock.c
	$(CC) $(CFLAGS) -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=0 -Wall -Wextra -fPIC -shared -o $@ $<

install: libslinit-mlock.so
	install -d $(DESTDIR)$(LIBDIR)
	install -m 0644 libslinit-mlock.so $(DESTDIR)$(LIBDIR)/libslinit-mlock.so

clean:
	rm -f libslinit-mlock.so

.PHONY: all install clean
