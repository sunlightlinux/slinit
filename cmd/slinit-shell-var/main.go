// slinit-shell-var — OpenRC-compatible env-var name sanitiser.
//
// Drop-in replacement for OpenRC's shell_var(1): every argv[i] is
// echoed with each non-alphanumeric byte replaced by '_'. Arguments
// are joined with a literal space (so `shell_var a.b c-d` → `a_b c_d`).
//
// Init.d scripts use it to derive shell-variable names from user-
// facing identifiers whose punctuation would otherwise be illegal:
//
//	svcname=$(shell_var "$RC_SVCNAME")   # my-thing.d/1  → my_thing_d_1
//	eval "${svcname}_PORT=8080"
package main

import (
	"bufio"
	"os"
)

func main() {
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for i, arg := range os.Args[1:] {
		if i != 0 {
			w.WriteByte(' ')
		}
		for j := 0; j < len(arg); j++ {
			c := arg[j]
			if !isAlnum(c) {
				c = '_'
			}
			w.WriteByte(c)
		}
	}
	w.WriteByte('\n')
}

func isAlnum(c byte) bool {
	return (c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z')
}
