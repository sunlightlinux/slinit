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

PAGES_8 = slinit.8 slinitctl.8
PAGES_5 = slinit-service.5
PAGES   = $(PAGES_8) $(PAGES_5)
# slinit-check.8 slinit-monitor.8 slinit-shutdown.8 — pending

all: $(PAGES)

%.8: %.8.md
	$(MD2MAN) -in $< -out $@

%.5: %.5.md
	$(MD2MAN) -in $< -out $@

install: $(PAGES)
	install -d $(DESTDIR)$(MANDIR)/man8
	install -m644 $(PAGES_8) $(DESTDIR)$(MANDIR)/man8/
	install -d $(DESTDIR)$(MANDIR)/man5
	install -m644 $(PAGES_5) $(DESTDIR)$(MANDIR)/man5/

clean:
	rm -f $(PAGES)

.PHONY: all install clean
