package process

import (
	"reflect"
	"testing"
)

// fullyPrivilegedDrops is what FullyPrivileged's doc comment says a "+"
// command runs without: run-as user and groups, capability / securebits
// / no-new-privs limits, LSM contexts, namespaces and the filesystem
// sandbox, the protect-* / restrict-* hardening, personality, seccomp.
var fullyPrivilegedDrops = map[string]bool{
	"RunAsUID": true, "RunAsGID": true, "SupplementaryGIDs": true,
	"NoNewPrivs": true, "AmbientCaps": true, "BoundingCaps": true, "Securebits": true,
	"AppArmorProfile": true, "SELinuxContext": true, "SMACKProcessLabel": true,
	"Cloneflags": true, "UidMappings": true, "GidMappings": true,
	"PrivateTmp": true, "PrivateDevices": true, "ProtectSystem": true, "ProtectHome": true,
	"ReadOnlyPaths": true, "ReadWritePaths": true, "InaccessiblePaths": true,
	"ProtectProc": true, "ProcSubset": true,
	"BindPaths": true, "BindReadOnlyPaths": true, "TemporaryFileSystem": true,
	"SeccompFilter": true, "SeccompArchitectures": true, "SeccompErrorAction": true,
	"SeccompLogFilter": true, "IOUringFilter": true,
	"ProtectKernelTunables": true, "ProtectKernelModules": true, "ProtectKernelLogs": true,
	"ProtectClock": true, "ProtectControlGroups": true, "ProtectHostname": true,
	"LockPersonality": true, "RestrictRealtime": true, "RestrictNamespaces": true,
	"RestrictSUIDSGID": true, "RestrictFileSystems": true,
	"RestrictAddressFamilies": true, "RestrictAFEnabled": true,
	"MemoryDenyWriteExecute": true, "Personality": true,
}

// fillNonZero sets every field of v to a non-zero value.
func fillNonZero(t *testing.T, v reflect.Value) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue // unexported
		}
		switch f.Kind() {
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			f.SetInt(7)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			f.SetUint(7)
		case reflect.String:
			f.SetString("x")
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		case reflect.Ptr:
			f.Set(reflect.New(f.Type().Elem()))
		case reflect.Func:
			f.Set(reflect.MakeFunc(f.Type(), func(in []reflect.Value) []reflect.Value {
				out := make([]reflect.Value, f.Type().NumOut())
				for j := range out {
					out[j] = reflect.Zero(f.Type().Out(j))
				}
				return out
			}))
		default:
			t.Fatalf("field %s: kind %s not handled", v.Type().Field(i).Name, f.Kind())
		}
	}
}

func TestFullyPrivilegedDropsOnlyTheDocumentedFields(t *testing.T) {
	var p ExecParams
	fillNonZero(t, reflect.ValueOf(&p).Elem())
	orig := p
	p.FullyPrivileged()

	pv, ov := reflect.ValueOf(p), reflect.ValueOf(orig)
	seen := 0
	for i := 0; i < pv.NumField(); i++ {
		name := pv.Type().Field(i).Name
		f, o := pv.Field(i), ov.Field(i)
		if !f.CanInterface() {
			continue // unexported
		}
		if fullyPrivilegedDrops[name] {
			seen++
			if !f.IsZero() {
				t.Errorf("%s survives FullyPrivileged: %v", name, f.Interface())
			}
			continue
		}
		// Everything else (cgroup, scheduling, rlimits, environment,
		// credentials, I/O wiring) is left exactly as it was.
		same := false
		if f.Kind() == reflect.Func {
			same = f.Pointer() == o.Pointer()
		} else {
			same = reflect.DeepEqual(f.Interface(), o.Interface())
		}
		if !same {
			t.Errorf("%s changed by FullyPrivileged", name)
		}
	}
	if seen != len(fullyPrivilegedDrops) {
		t.Errorf("%d of the %d dropped fields exist on ExecParams; the list is stale",
			seen, len(fullyPrivilegedDrops))
	}
}

// Spot-check the settings systemd keeps for a "+" command by value.
func TestFullyPrivilegedKeepsCgroupSchedAndCredentials(t *testing.T) {
	nice := 5
	p := ExecParams{
		RunAsUID: 1000, CgroupPath: "/sys/fs/cgroup/x", Nice: &nice,
		Rlimits:     []Rlimit{{Resource: 7, Soft: 1, Hard: 2}},
		Env:         []string{"A=1"},
		Credentials: []CredentialSource{{Name: "c", Value: "v"}},
		SchedPolicy: 3, CPUAffinity: []uint{1},
	}
	p.FullyPrivileged()
	if p.RunAsUID != 0 || p.CgroupPath != "/sys/fs/cgroup/x" || p.Nice != &nice ||
		len(p.Rlimits) != 1 || len(p.Env) != 1 || len(p.Credentials) != 1 ||
		p.SchedPolicy != 3 || len(p.CPUAffinity) != 1 {
		t.Errorf("unexpected result: %+v", p)
	}
}
