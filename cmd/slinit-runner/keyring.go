package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// applyKeyringMode is systemd's KeyringMode=: private gives the service
// a new anonymous session keyring, so keys it adds are its own rather
// than in the one slinit (and every other service) inherited; shared
// links the user's keyring into it, so the service sees that user's
// keys as well. Runs as the service's user.
func applyKeyringMode(mode string) error {
	switch mode {
	case "private", "shared":
	default:
		return fmt.Errorf("unknown mode %q (want private or shared)", mode)
	}
	// KEYCTL_JOIN_SESSION_KEYRING with a NULL name: a new anonymous one.
	// Not there (no CONFIG_KEYS), refused (a container's seccomp
	// profile) or over the user's key quota: the service starts with
	// the keyring it has, as systemd's setup_keyring goes on for
	// ENOSYS, EPERM, EACCES and EDQUOT. Anything else fails it.
	if err := joinSessionKeyring(); err != nil {
		switch err {
		case unix.ENOSYS, unix.EPERM, unix.EACCES, unix.EDQUOT:
			fmt.Fprintf(os.Stderr, "slinit-runner: keyring-mode=%s: no session keyring of its own (%v); keeping the inherited one\n", mode, err)
			return nil
		}
		return fmt.Errorf("new session keyring: %w", err)
	}
	if mode == "shared" {
		if _, err := unix.KeyctlInt(unix.KEYCTL_LINK, unix.KEY_SPEC_USER_KEYRING, unix.KEY_SPEC_SESSION_KEYRING, 0, 0); err != nil {
			return fmt.Errorf("link the user keyring: %w", err)
		}
	}
	return nil
}

// joinSessionKeyring is KEYCTL_JOIN_SESSION_KEYRING with a NULL name; a
// var so a test can have it fail as a container's filter makes it.
var joinSessionKeyring = func() error {
	_, err := unix.KeyctlInt(unix.KEYCTL_JOIN_SESSION_KEYRING, 0, 0, 0, 0)
	return err
}
