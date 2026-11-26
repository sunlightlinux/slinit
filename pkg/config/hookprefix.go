package config

import (
	"fmt"
	"strings"
)

// The command settings that stand for a systemd Exec*= line accept
// systemd's "+" prefix, "run with full privileges": pre-start-command
// (ExecStartPre=), post-start-command (ExecStartPost=), stop-command
// (ExecStop=), finish-command (ExecStopPost=), pre-stop-hook,
// control-command-* and exec-condition (ExecCondition=). A "+" command
// runs as root rather than as the service's run-as user; see
// service.PrivilegedHooks for what else it leaves out.

// cutPrivilegedPrefix reads the "+" prefix off a command value. It
// must be followed directly by the program, as in systemd: "+ /bin/x"
// and a bare "+" are refused rather than run "+" or " /bin/x".
func cutPrivilegedPrefix(setting, value string) (string, bool, error) {
	v := strings.TrimLeft(value, " \t")
	if !strings.HasPrefix(v, "+") {
		return value, false, nil
	}
	v = v[1:]
	if v == "" || v[0] == ' ' || v[0] == '\t' {
		return "", false, fmt.Errorf("%s: \"+\" must be followed directly by the command", setting)
	}
	return v, true, nil
}

// setPrivilegedCommand applies "setting = value" or "setting += value"
// to a command setting that accepts the "+" prefix. "=" sets the
// command and its flag; "+=" appends arguments and keeps the flag, so
// a "+" there is an argument like any other. A "+=" to a command not
// yet set starts it, as "=" would, "+" included.
func setPrivilegedCommand(cmd *[]string, priv *bool, setting, value string, op OperatorType, serviceArg *string) error {
	if op == OpPlusEqual && len(*cmd) > 0 {
		*cmd = append(*cmd, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		return nil
	}
	v, p, err := cutPrivilegedPrefix(setting, value)
	if err != nil {
		return err
	}
	*cmd = splitCommand(expandEnvVarsForCommand(v, serviceArg))
	*priv = p
	return nil
}
