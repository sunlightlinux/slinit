# slinit man pages.
#
# Source is markdown; rendered to roff via go-md2man (pinned as a Go tool
# in go.mod, so no system dependency beyond the Go toolchain itself).
#
# Usage:
#   make            render all pages
#   make install    install under $(DESTDIR)$(MANDIR)
#   make clean      remove rendered output

PREFIX  ?= /usr/local
MANDIR  ?= $(PREFIX)/share/man

MD2MAN  ?= go tool github.com/cpuguy83/go-md2man/v2

PAGES_8 = \
	slinit.8 \
	slinit-runner.8 \
	slinitctl.8 \
	slinit-check.8 \
	slinit-monitor.8 \
	slinit-shutdown.8 \
	slinit-checkpath.8 \
	slinit-init-maker.8 \
	slinit-mount.8 \
	slinit-nuke.8 \
	rc-service.8 \
	rc-update.8 \
	rc-status.8

PAGES_7 = slinit-resource.7

PAGES_5 = slinit-service.5

PAGES   = $(PAGES_8) $(PAGES_7) $(PAGES_5)

# Aliases installed as symlinks pointing at slinit-shutdown.8.
# These mirror the symlinks the binary itself ships with, so
# `man reboot` (etc.) lands on the right page.
SHUTDOWN_ALIASES = slinit-reboot.8 slinit-halt.8 slinit-poweroff.8 slinit-soft-reboot.8

all: $(PAGES)

%.8: %.8.md
	$(MD2MAN) -in $< -out $@

%.7: %.7.md
	$(MD2MAN) -in $< -out $@

%.5: %.5.md
	$(MD2MAN) -in $< -out $@

install: $(PAGES)
	install -d $(DESTDIR)$(MANDIR)/man8
	install -m644 $(PAGES_8) $(DESTDIR)$(MANDIR)/man8/
	for alias in $(SHUTDOWN_ALIASES); do \
		ln -sf slinit-shutdown.8 $(DESTDIR)$(MANDIR)/man8/$$alias; \
	done
	install -d $(DESTDIR)$(MANDIR)/man7
	install -m644 $(PAGES_7) $(DESTDIR)$(MANDIR)/man7/
	install -d $(DESTDIR)$(MANDIR)/man5
	install -m644 $(PAGES_5) $(DESTDIR)$(MANDIR)/man5/

clean:
	rm -f $(PAGES)

.PHONY: all install clean
