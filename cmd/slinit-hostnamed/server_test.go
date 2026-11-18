package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/hostname"
	"github.com/sunlightlinux/slinit/pkg/polkit"
)

// world is a scratch machine: /etc and DMI files in a temp tree, a fake
// kernel hostname, and a polkit that answers as told and remembers what it
// was asked. Nothing here reads or changes the real hostname.
type world struct {
	t      *testing.T
	root   string
	s      *server
	allow  bool
	asked  []string
	kernel string
}

func (w *world) write(path, content string) {
	w.t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, root: t.TempDir(), allow: true, kernel: "boot-name"}
	p := func(rel string) string { return filepath.Join(w.root, rel) }
	for ptr, v := range map[*string]string{
		&hostname.HostnamePath:           p("etc/hostname"),
		&hostname.MachineInfoPath:        p("etc/machine-info"),
		&hostname.OSReleasePath:          p("etc/os-release"),
		&hostname.MachineIDPath:          p("etc/machine-id"),
		&hostname.BootIDPath:             p("proc/boot_id"),
		&hostname.DMISysVendorPath:       p("dmi/sys_vendor"),
		&hostname.DMIProductNamePath:     p("dmi/product_name"),
		&hostname.DMIChassisTypePath:     p("dmi/chassis_type"),
		&hostname.DMIBiosVendorPath:      p("dmi/bios_vendor"),
		&hostname.DMIBiosVersionPath:     p("dmi/bios_version"),
		&hostname.DMIBiosDatePath:        p("dmi/bios_date"),
		&hostname.DMIProductSKUPath:      p("dmi/product_sku"),
		&hostname.DMIProductVersionPath:  p("dmi/product_version"),
		&hostname.DMIChassisAssetTagPath: p("dmi/chassis_asset_tag"),
		&hostname.DMIProductUUIDPath:     p("dmi/product_uuid"),
		&hostname.DMIProductSerialPath:   p("dmi/product_serial"),
		&hostname.DMIBoardSerialPath:     p("dmi/board_serial"),
		&hostname.VSockDevicePath:        p("dev/vsock"),
		&hostname.Proc1EnvironPath:       p("proc/1/environ"),
		&hostname.Proc1CgroupPath:        p("proc/1/cgroup"),
	} {
		old := *ptr
		*ptr = v
		t.Cleanup(func() { *ptr = old })
	}
	oldK, oldSet := hostname.KernelHostname, hostname.SetKernelHostname
	hostname.KernelHostname = func() string { return w.kernel }
	hostname.SetKernelHostname = func(n string) (bool, error) {
		if w.kernel == n {
			return false, nil
		}
		w.kernel = n
		return true, nil
	}
	t.Cleanup(func() { hostname.KernelHostname, hostname.SetKernelHostname = oldK, oldSet })

	w.write(p("etc/os-release"), "NAME=\"Sunlight Linux\"\nPRETTY_NAME=\"Sunlight Linux\"\nDEFAULT_HOSTNAME=sunlight\nHOME_URL=\"https://example.org/\"\nSUPPORT_END=2030-01-31\n")
	w.write(p("etc/machine-id"), "0123456789abcdef0123456789abcdef\n")
	w.write(p("dmi/sys_vendor"), "LENOVO\n")
	w.write(p("dmi/product_name"), "21AH\n")
	w.write(p("dmi/bios_version"), "N3MET18W (1.17 )\n")
	w.write(p("dmi/bios_date"), "05/23/2024\n")
	w.write(p("dmi/product_uuid"), "4c4c4544-0042-3510-8048-b7c04f565431\n")
	w.write(p("dmi/product_serial"), "PF3ABCDE\n")

	w.s = newServer(nil, &polkit.Checker{
		UID: func(dbus.Sender) (uint32, error) { return 1000, nil },
		Check: func(_ dbus.Sender, action string, _ map[string]string, _ bool) (bool, bool, error) {
			w.asked = append(w.asked, action)
			return w.allow, !w.allow, nil
		},
	})
	return w
}

func errName(e *dbus.Error) string {
	if e == nil {
		return ""
	}
	return e.Name
}

func prop(t *testing.T, s *server, name string) any {
	t.Helper()
	v, ok := s.property(name)
	if !ok {
		t.Fatalf("no property %s", name)
	}
	return v.Value()
}

func TestHostnameValidationIsSystemds(t *testing.T) {
	for name, want := range map[string]bool{
		"sunlight": true, "localhost": true, "a.b-c.d": true, "x": true,
		"a.-b": false, "a-.b": false, "-a": false, "a-": false, "a..b": false,
		"a.": false, ".a": false, "a_b": false, "": false, strings.Repeat("a", 65): false,
	} {
		if got := hostname.Valid(name); got != want {
			t.Errorf("Valid(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSetStaticHostname(t *testing.T) {
	w := newWorld(t)
	if got := errName(w.s.SetStaticHostname(":1.9", "a.-b", true)); got != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Errorf("invalid name: %q", got)
	}
	w.allow = false
	if errName(w.s.SetStaticHostname(":1.9", "desk", true)) == "" || hostname.ReadStatic() != "" {
		t.Fatal("a refused request wrote /etc/hostname")
	}
	w.allow = true
	if err := w.s.SetStaticHostname(":1.9", "desk", true); err != nil {
		t.Fatal(err)
	}
	if hostname.ReadStatic() != "desk" || w.kernel != "desk" {
		t.Errorf("static=%q kernel=%q, want both desk", hostname.ReadStatic(), w.kernel)
	}
	if prop(t, w.s, "HostnameSource") != "static" || w.asked[len(w.asked)-1] != iface+".set-static-hostname" {
		t.Errorf("source %v, asked %v", prop(t, w.s, "HostnameSource"), w.asked)
	}
	n := len(w.asked)
	if err := w.s.SetStaticHostname(":1.9", "desk", true); err != nil || len(w.asked) != n {
		t.Error("setting the same static name must not ask polkit")
	}
	// An empty name removes /etc/hostname; the kernel falls back to the
	// default, os-release's DEFAULT_HOSTNAME.
	if err := w.s.SetStaticHostname(":1.9", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hostname.HostnamePath); !os.IsNotExist(err) {
		t.Error("/etc/hostname not removed")
	}
	if w.kernel != "sunlight" || prop(t, w.s, "HostnameSource") != "default" {
		t.Errorf("kernel=%q source=%v, want sunlight/default", w.kernel, prop(t, w.s, "HostnameSource"))
	}
}

// The transient name only takes effect while /etc/hostname names none.
func TestSetHostnameIsTransient(t *testing.T) {
	w := newWorld(t)
	if err := w.s.SetHostname(":1.9", "dhcp-name", true); err != nil {
		t.Fatal(err)
	}
	if w.kernel != "dhcp-name" || prop(t, w.s, "HostnameSource") != "transient" {
		t.Errorf("kernel=%q source=%v", w.kernel, prop(t, w.s, "HostnameSource"))
	}
	w.write(hostname.HostnamePath, "# comment\n\nfixed\n")
	if err := w.s.SetHostname(":1.9", "other", true); err != nil {
		t.Fatal(err)
	}
	if w.kernel != "fixed" || prop(t, w.s, "HostnameSource") != "static" {
		t.Errorf("with a static name the transient one is ignored: kernel=%q", w.kernel)
	}
	if w.asked[0] != iface+".set-hostname" {
		t.Errorf("asked %v", w.asked)
	}
}

func TestMachineInfoSetters(t *testing.T) {
	w := newWorld(t)
	if err := w.s.SetPrettyHostname(":1.9", "Ionut's \"Desk\"", true); err != nil {
		t.Fatal(err)
	}
	if prop(t, w.s, "PrettyHostname") != "Ionut's \"Desk\"" {
		t.Errorf("pretty = %v", prop(t, w.s, "PrettyHostname"))
	}
	if w.asked[0] != iface+".set-static-hostname" {
		t.Errorf("the pretty name uses the static hostname's action, asked %v", w.asked)
	}
	for _, c := range []struct {
		set  func(string) *dbus.Error
		bad  string
		good string
		prop string
	}{
		{func(v string) *dbus.Error { return w.s.SetChassis(":1.9", v, true) }, "spaceship", "laptop", "Chassis"},
		{func(v string) *dbus.Error { return w.s.SetIconName(":1.9", v, true) }, "../etc/x", "computer-desk", "IconName"},
		{func(v string) *dbus.Error { return w.s.SetDeployment(":1.9", v, true) }, "prod env", "production", "Deployment"},
		{func(v string) *dbus.Error { return w.s.SetLocation(":1.9", v, true) }, "line\nbreak", "Rack 4, \"top\"", "Location"},
	} {
		if got := errName(c.set(c.bad)); got != "org.freedesktop.DBus.Error.InvalidArgs" {
			t.Errorf("%s %q: %q", c.prop, c.bad, got)
		}
		if err := c.set(c.good); err != nil {
			t.Fatalf("%s %q: %v", c.prop, c.good, err)
		}
		if prop(t, w.s, c.prop) != c.good {
			t.Errorf("%s = %v, want %q", c.prop, prop(t, w.s, c.prop), c.good)
		}
	}
	if last := w.asked[len(w.asked)-1]; last != iface+".set-machine-info" {
		t.Errorf("asked %s", last)
	}
	// An empty value removes the field.
	if err := w.s.SetLocation(":1.9", "", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(hostname.MachineInfoPath); strings.Contains(string(b), "LOCATION") {
		t.Errorf("LOCATION left in machine-info:\n%s", b)
	}
}

func TestProperties(t *testing.T) {
	w := newWorld(t)
	w.write(hostname.MachineInfoPath, "CHASSIS=laptop\nHARDWARE_MODEL=\"ThinkPad X1\"\n")
	for name, want := range map[string]any{
		"IconName":                  "computer-laptop",
		"Chassis":                   "laptop",
		"HardwareVendor":            "LENOVO",
		"HardwareModel":             "ThinkPad X1", // machine-info overrides DMI
		"FirmwareVersion":           "N3MET18W (1.17 )",
		"OperatingSystemPrettyName": "Sunlight Linux",
		"HomeURL":                   "https://example.org/",
		"DefaultHostname":           "sunlight",
		"StaticHostname":            "",
		"Hostname":                  "boot-name",
		"HardwareSKU":               "",
	} {
		if got := prop(t, w.s, name); got != want {
			t.Errorf("%s = %#v, want %#v", name, got, want)
		}
	}
	if prop(t, w.s, "FirmwareDate").(uint64) == math.MaxUint64 || prop(t, w.s, "OperatingSystemSupportEnd").(uint64) == math.MaxUint64 {
		t.Error("known dates reported as unknown")
	}
	os.Remove(hostname.DMIBiosDatePath)
	if prop(t, w.s, "FirmwareDate").(uint64) != math.MaxUint64 {
		t.Error("an unknown firmware date must be UINT64_MAX, as systemd reports it")
	}
	if id := prop(t, w.s, "MachineID").([]byte); len(id) != 16 || id[0] != 0x01 {
		t.Errorf("MachineID = %x", id)
	}
	if b := prop(t, w.s, "BootID").([]byte); len(b) != 0 {
		t.Errorf("a missing boot_id must be an empty array, got %x", b)
	}
	if prop(t, w.s, "VSockCID") != uint32(0xffffffff) {
		t.Error("no vsock: VSockCID must be VMADDR_CID_ANY")
	}
	all, _ := w.s.svc.Properties().GetAll(iface)
	if len(all) != len(properties) {
		t.Errorf("GetAll has %d of %d properties", len(all), len(properties))
	}
}

func TestTags(t *testing.T) {
	w := newWorld(t)
	if got := errName(w.s.SetTags(":1.9", []string{"-bad"})); got != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Errorf("invalid tag: %q", got)
	}
	if err := w.s.SetTags(":1.9", []string{"role=web", "gpu", "role=db"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(prop(t, w.s, "Tags").([]string), ","); got != "gpu,role=db" {
		t.Errorf("tags %q: dedup by key, last value wins, sorted", got)
	}
	if err := w.s.AddAndRemoveTags(":1.9", []string{"site=bucharest"}, []string{"ro*"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(prop(t, w.s, "Tags").([]string), ","); got != "gpu,site=bucharest" {
		t.Errorf("tags %q", got)
	}
	if mi, _ := hostname.LoadMachineInfo(); mi.Get("TAGS") != "gpu:site=bucharest" {
		t.Errorf("TAGS in machine-info = %q", mi.Get("TAGS"))
	}
}

func TestPrivilegedReads(t *testing.T) {
	w := newWorld(t)
	w.allow = false
	if _, err := w.s.GetProductUUID(":1.9", false); errName(err) != "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired" {
		t.Errorf("refused UUID: %v", err)
	}
	d, err := w.s.Describe(":1.9")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d, "PF3ABCDE") || strings.Contains(d, "ProductUUID") {
		t.Errorf("unprivileged Describe leaks the serial or UUID: %s", d)
	}
	w.allow = true
	u, err := w.s.GetProductUUID(":1.9", true)
	if err != nil || len(u) != 16 || u[0] != 0x4c {
		t.Fatalf("UUID %x %v", u, err)
	}
	if v, err := w.s.GetHardwareSerial(":1.9"); err != nil || v != "PF3ABCDE" {
		t.Errorf("serial %q %v", v, err)
	}
	d, _ = w.s.Describe(":1.9")
	var m map[string]any
	if err := json.Unmarshal([]byte(d), &m); err != nil {
		t.Fatal(err)
	}
	if m["HardwareSerial"] != "PF3ABCDE" || m["MachineID"] != "0123456789abcdef0123456789abcdef" || m["OperatingSystemHomeURL"] != "https://example.org/" {
		t.Errorf("privileged Describe: %v", m)
	}
	os.Remove(hostname.DMIProductUUIDPath)
	if _, err := w.s.GetProductUUID(":1.9", true); errName(err) != iface+".NoProductUUID" {
		t.Errorf("no UUID: %v", err)
	}
	if _, err := w.s.GetMachineInfo("NOPE"); errName(err) != iface+".FieldNotSet" {
		t.Errorf("GetMachineInfo unset: %v", err)
	}
	if _, err := w.s.GetMachineInfo("bad-name"); errName(err) != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Errorf("GetMachineInfo bad name: %v", err)
	}
}

// Every action asked must be declared by the shipped policy, and every
// declared one asked.
func TestPolicyDeclaresEveryActionAsked(t *testing.T) {
	b, err := os.ReadFile("../../data/polkit-1/actions/org.freedesktop.hostname1.policy")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`<action id="([^"]+)">`).FindAllStringSubmatch(string(b), -1) {
		declared[m[1]] = true
	}
	w := newWorld(t)
	w.s.SetHostname(":1.9", "a", true)
	w.s.SetStaticHostname(":1.9", "b", true)
	w.s.SetChassis(":1.9", "server", true)
	w.s.GetProductUUID(":1.9", true)
	w.s.GetHardwareSerial(":1.9")
	w.s.Describe(":1.9")
	asked := map[string]bool{}
	for _, a := range w.asked {
		asked[a] = true
		if !declared[a] {
			t.Errorf("daemon asks %s, which the policy does not declare", a)
		}
	}
	for a := range declared {
		if !asked[a] {
			t.Errorf("policy declares %s, which the daemon never asks", a)
		}
	}
}
