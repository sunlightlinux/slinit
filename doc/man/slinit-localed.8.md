% SLINIT-LOCALED(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-11-18

# NAME

slinit-localed - system locale and keyboard service for slinit

# SYNOPSIS

**slinit-localed** [**-exit-idle** *duration*]

# DESCRIPTION

**slinit-localed** implements *org.freedesktop.locale1* on the system
bus: the interface GNOME Settings' Region panel uses to set the system
language and the login screen's keyboard, and that GNOME Shell's login
screen, mutter and gnome-settings-daemon read the keyboard layout from.
It is systemd-localed's interface, so a desktop built for systemd works
unchanged.

The interface belongs to another project, so upstream's definition is
the contract, not slinit's current behaviour — see **STABILITY.md**.

It reads and writes the same files as **slinit-localectl**(1), with the
same code: */etc/locale.conf* for the locale, */etc/vconsole.conf* for
the console keymap (*KEYMAP*, *KEYMAP_TOGGLE*) and the X11 layout
(*XKBLAYOUT*, *XKBMODEL*, *XKBVARIANT*, *XKBOPTIONS*), and
*/etc/X11/xorg.conf.d/00-keyboard.conf*, which it keeps in step for Xorg.
As in systemd, the X11 layout is read from *vconsole.conf* when that names
one, else from *00-keyboard.conf*. Other lines in those files, such as
*FONT=*, are kept.

Setting the console keymap also rewrites an active **KEYMAP=** in
*/etc/rc.conf*, which slinit-services' boot script loads on every boot,
and loads the new keymap at once with **loadkeys**(1).

It is started by D-Bus activation and exits after **-exit-idle** without
requests (30 seconds by default; *0* keeps it running).

What it changes, and why a start failed, is logged to syslog as well as
stderr, since its stderr is dbus-daemon's and usually goes nowhere; read
it with **slinit-journalctl -t slinit-localed** (since 3.21.2).

# CONVERSION

With *convert* set, **SetVConsoleKeyboard** also sets the X11 layout and
**SetX11Keyboard** the console keymap, through systemd's *kbd-model-map*;
and **SetLocale** fills *LANGUAGE* from *language-fallback-map* when only
*LANG* is given. Both are systemd's data files, looked up in
*/usr/share/slinit/* and then */usr/share/systemd/*. Sunlight ships
neither, so no conversion is found and the other setting is left as it
is, which is what systemd does when its map has no matching row.

# AUTHORIZATION

Changes are authorized by polkit with systemd's action ids:
*org.freedesktop.locale1.set-locale* for **SetLocale** (it implies the
next one) and *org.freedesktop.locale1.set-keyboard* for both keyboard
methods. By default each needs an administrator's password
(*auth_admin_keep*). Root is always allowed; when polkitd is not running,
nobody else is. Whether anything would change is decided before polkit is
asked, and worked out again from the files once it has answered, so a
change made while the caller was typing a password is not overwritten.

# D-BUS INTERFACE

Bus name *org.freedesktop.locale1*, object */org/freedesktop/locale1*.

**SetLocale**(*as* locale, *b* interactive)
:   A single bare locale sets *LANG*; otherwise each item is *VAR=value*
    for one of *LANG*, *LANGUAGE* and the *LC_\** variables, each at most
    once. Every locale must be installed (as **locale -a** lists it).
    *LANGUAGE* is a colon-separated list of locale names, which need not
    be installed.
    Variables not given keep their value, and any equal to *LANG* is
    dropped, as systemd-localed merges them.

**SetVConsoleKeyboard**(*s* keymap, *s* keymap_toggle, *b* convert, *b* interactive)
:   The console keymap and its toggle; each must be installed under
    */usr/share/kbd/keymaps* (or */usr/share/keymaps*, */usr/lib/kbd/keymaps*).

**SetX11Keyboard**(*s* layout, *s* model, *s* variant, *s* options, *b* convert, *b* interactive)
:   The X11 layout. The values are checked for safe characters; without
    libxkbcommon they are not compiled, as systemd skips that check
    when the library is missing.

Properties, all read-only and announced with *PropertiesChanged*:
**Locale** *as*, **VConsoleKeymap** *s*, **VConsoleKeymapToggle** *s*,
**X11Layout** *s*, **X11Model** *s*, **X11Variant** *s*, **X11Options**
*s*.

# FILES

*/usr/share/dbus-1/system.d/org.freedesktop.locale1.conf*,
*/usr/share/dbus-1/system-services/org.freedesktop.locale1.service*,
*/usr/share/polkit-1/actions/org.freedesktop.locale1.policy*
:   Bus policy, activation and polkit actions.

*/etc/locale.conf*, */etc/vconsole.conf*,
*/etc/X11/xorg.conf.d/00-keyboard.conf*, */etc/rc.conf*
:   What it reads and writes.

# EXIT STATUS

0 after an idle exit; 1 if the system bus cannot be reached or the name
is already owned.

# SEE ALSO

**slinit-localectl**(1), **slinit-timedated**(8), **slinit-hostnamed**(8),
**locale.conf**(5), **vconsole.conf**(5), **polkit**(8)
