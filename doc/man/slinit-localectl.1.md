% SLINIT-LOCALECTL(1) slinit | Sunlight Linux
% Ionut Nechita
% 2026-11-18

# NAME

slinit-localectl - query or change the system locale and keyboard

# SYNOPSIS

**slinit-localectl** [*OPTIONS*...] [*COMMAND*] [*ARG*...]

# DESCRIPTION

**slinit-localectl** is systemd's **localectl**(1) for slinit. It is
usually installed as **localectl** too.

Like **slinit-hostnamectl** and **slinit-timedatectl**, it reads and
writes the files directly and needs no bus, so it works in a rescue
shell; desktops go through **slinit-localed**(8), which applies the same
rules with the same code. Changes need write access to */etc*, so they
are made as root.

The locale is */etc/locale.conf*. The console keymap and the X11 layout
are */etc/vconsole.conf* (*KEYMAP*, *KEYMAP_TOGGLE*, *XKBLAYOUT*,
*XKBMODEL*, *XKBVARIANT*, *XKBOPTIONS*), with
*/etc/X11/xorg.conf.d/00-keyboard.conf* kept in step for Xorg. Setting the
console keymap also updates an active **KEYMAP=** in */etc/rc.conf*, which
slinit-services' boot script loads, and loads the keymap at once.

# COMMANDS

**status**
:   Show the locale, the console keymap and the X11 layout. The default.

**set-locale** *LOCALE*...
:   A single *LOCALE* sets *LANG*; otherwise each argument is
    *VAR=value*. Locales must be installed. Variables not given keep
    their value; any equal to *LANG* is dropped.

**list-locales**
:   The installed locales (**locale -a**).

**set-keymap** *MAP* [*TOGGLEMAP*]
:   Set the console keymap. Unless **--no-convert**, the X11 layout
    follows when *kbd-model-map* has a row for it (see
    **slinit-localed**(8), CONVERSION).

**list-keymaps**
:   The installed console keymaps.

**set-x11-keymap** *LAYOUT* [*MODEL* [*VARIANT* [*OPTIONS*]]]
:   Set the X11 layout. Unless **--no-convert**, the console keymap
    follows when *kbd-model-map* has a row for it.

**list-x11-keymap-models**, **list-x11-keymap-layouts**, **list-x11-keymap-options**, **list-x11-keymap-variants** [*LAYOUT*]
:   From xkeyboard-config's */usr/share/X11/xkb/rules/base.lst*.

# OPTIONS

**--no-convert**
:   Change only the keyboard setting named.

**-h**, **--help**; **--version**
:   Help, version.

**--no-pager**, **--no-ask-password**
:   Accepted for compatibility; nothing to do here.

**-H**, **--host**, **-M**, **--machine**
:   Not supported.

# EXIT STATUS

0 on success, 1 when a command fails, 2 for bad arguments.

# SEE ALSO

**slinit-localed**(8), **slinit-hostnamectl**(1),
**slinit-timedatectl**(1), **locale.conf**(5), **vconsole.conf**(5)
