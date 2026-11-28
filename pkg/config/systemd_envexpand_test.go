package config

import (
	"math/rand"
	"os/exec"
	"strings"
	"testing"
)

// modelReplaceEnvArgv is systemd's replace_env_argv and
// replace_env_full (src/basic/env-util.c, flags 0) transcribed state by
// state, as the reference the conversion is checked against. Values are
// split on blanks only, which is all the values used here need.
func modelReplaceEnvArgv(argv []string, env map[string]string) []string {
	var out []string
	for _, w := range argv {
		if w != "" && w[0] == '$' && (len(w) == 1 || w[1] != '{' && w[1] != '$') {
			if isEnvName(w[1:]) {
				if v, ok := env[w[1:]]; ok {
					out = append(out, strings.Fields(v)...)
				}
			}
			continue
		}
		out = append(out, modelReplaceEnvFull(w, env))
	}
	return out
}

func modelReplaceEnvFull(f string, env map[string]string) string {
	const (
		word = iota
		curly
		variable
	)
	state, start := word, 0
	var s strings.Builder
	for e := 0; e < len(f); e++ {
		switch c := f[e]; state {
		case word:
			if c == '$' {
				state = curly
			}
		case curly:
			switch c {
			case '{':
				s.WriteString(f[start : e-1])
				start, state = e-1, variable
			case '$':
				s.WriteString(f[start:e])
				start, state = e+1, word
			default:
				state = word
			}
		case variable:
			switch c {
			case '}':
				if name := f[start+2 : e]; isEnvName(name) {
					s.WriteString(env[name])
				}
				start, state = e+1, word
			case ':':
				state = word
			}
		}
	}
	s.WriteString(f[start:])
	return s.String()
}

// The words of an Exec line, written in slinit's $ syntax and read back
// by slinit's parser, expanded from slinit's environment, are what
// systemd's own substitution makes of them in the same environment;
// with ":" they are the words as written.
func TestSystemdEnvArgvMatchesSystemd(t *testing.T) {
	env := map[string]string{"SDA": "va", "SDB": "", "SDS": "two words"}
	for k, v := range env {
		t.Setenv(k, v)
	}
	// SDC is unset; SDS has a blank, which slinit splits a whole-word
	// $VAR at as systemd does (and keeps in one ${VAR} argument).
	tokens := []string{"$", "$", "$", "{", "}", ":", "-", "+", "SDA", "SDB", "SDC", "SDS", "1", "x",
		" ", `"`, "'", `\`, "/", "@", "$$", "${SDA}", "${SDC}", "${SDS}", "$SDA", "${", "é"}
	r := rand.New(rand.NewSource(1))
	fails := 0
	for n := 0; n < 100000 && fails < 10; n++ {
		words := make([]string, 1+r.Intn(4))
		for i := range words {
			var b strings.Builder
			for k := r.Intn(6); k > 0; k-- {
				b.WriteString(tokens[r.Intn(len(tokens))])
			}
			words[i] = b.String()
		}
		for _, literal := range []bool{false, true} {
			want := append([]string{"/bin/p"}, words...)
			if !literal {
				want = append([]string{"/bin/p"}, modelReplaceEnvArgv(words, env)...)
			}
			conv, _, _ := systemdEnvArgv(words, literal)
			value, ok := slinitCommandValue(append([]string{"/bin/p"}, conv...))
			if !ok {
				t.Fatalf("%q: no value", words)
			}
			d, err := Parse(strings.NewReader("type = process\ncommand = "+value+"\n"), "p", "p")
			if err != nil {
				t.Errorf("%q (literal %v) -> %q: %v", words, literal, value, err)
				fails++
				continue
			}
			if strings.Join(d.Command, "\x01") != strings.Join(want, "\x01") || len(d.Command) != len(want) {
				t.Errorf("%q (literal %v) -> %q -> %q, want %q", words, literal, value, d.Command, want)
				fails++
			}
		}
	}
}

// systemd substitutes a word that is "$NAME" alone and "${NAME}"
// anywhere; every other $ is text. The conversion used to escape only
// "$/" and "$$", so slinit expanded at load time what systemd leaves
// alone: the $HOME and $1 of a shell script lost their meaning, and
// "${A:-x}", which systemd does not support, became bash's default.
func TestSystemdExecVarsAsSystemd(t *testing.T) {
	t.Setenv("SDV_A", "val")
	t.Setenv("HOME", "/root-at-load")
	unit := `[Service]
ExecStart=/bin/echo "x${SDV_A:-dflt}" "a$1" "$1x" "a$SDV_A" $SDV_A ${SDV_A} "${SDV_UNSET}" $HOME/x "${1}" $ "a$" "$$SDV_A" "${SDV_A"
ExecStartPre=/bin/sh -c 'echo $HOME $1 "$@"'
ExecStop=|echo $SDV_A "$$" 'a $HOME'
`
	want := []string{"/bin/echo", "x${SDV_A:-dflt}", "a$1", "a$SDV_A", "val", "val", "", "", "a$", "$SDV_A", "${SDV_A"}
	direct, got := loadSystemdDirect(t, unit)
	converted, warns := convertWarns(t, unit)
	// Since 4.1 the variables are expanded when the command runs, from
	// the service's environment (runtimeShellArgv): what the program is
	// given then is what systemd gives it. SDV_A=val there, set above
	// for the old load-time expansion too.
	for name, d := range map[string]*ServiceDescription{"converted": converted, "direct": direct} {
		argvIs(t, name+": command", runtimeArgv(t, d.Command, "SDV_A=val"), want)
		argvIs(t, name+": pre-start-command", d.PreStartCommand, []string{"/bin/sh", "-c", `echo $HOME $1 "$@"`})
		// "|" substitutes word by word, then joins the words.
		argvIs(t, name+": stop-command", runtimeArgv(t, d.StopCommand, "SDV_A=val"), []string{"/bin/sh", "-c", "echo val $ a $HOME"})
	}
	var refs, bad bool
	for _, w := range warns {
		refs = refs || w.Level == "NOTE" && strings.Contains(w.Msg, "ExecStart= uses $SDV_A ${SDV_A} ${SDV_UNSET} —")
		bad = bad || w.Level == "WARN" && strings.Contains(w.Msg, "ExecStart= uses $1x $HOME/x ${1} $, no valid variable name")
		if strings.Contains(w.Msg, "ExecStartPre= uses") {
			t.Errorf("ExecStartPre= has no variable systemd substitutes, yet: %v", w)
		}
	}
	if !refs || !bad {
		t.Errorf("want a NOTE on $SDV_A ${SDV_A} ${SDV_UNSET} and a WARN on $1x $HOME/x ${1} $: %v", warns)
	}
	if len(got) == 0 {
		t.Errorf("the direct load gave no warnings")
	}
}

// runtimeArgv is what a command made by runtimeShellArgv hands its
// program when it runs with env: its words, through the shell, printed
// one per NUL. Any other command is returned as it is.
func runtimeArgv(t *testing.T, cmd []string, env ...string) []string {
	t.Helper()
	if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" || !strings.HasPrefix(cmd[2], "set -f; exec ") {
		return cmd
	}
	script := "set -f; set -- " + strings.TrimPrefix(cmd[2], "set -f; exec ") + `; printf '%s\0' "$@"`
	c := exec.Command("/bin/sh", "-c", script)
	c.Env = env
	out, err := c.Output()
	if err != nil {
		t.Fatalf("run %q: %v", script, err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}
