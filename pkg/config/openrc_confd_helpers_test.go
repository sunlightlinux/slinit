package config

import "os"

// Tiny test helpers kept in a separate file so openrc_confd_test.go
// focuses on assertions. Used only by the openrc_confd test suite.

func openForWrite(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
}

func chmodExec(path string) error {
	return os.Chmod(path, 0755)
}
