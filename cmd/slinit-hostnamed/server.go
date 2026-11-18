package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/dbusservice"
	"github.com/sunlightlinux/slinit/pkg/hostname"
	"github.com/sunlightlinux/slinit/pkg/polkit"
)

const (
	busName = "org.freedesktop.hostname1"
	objPath = dbus.ObjectPath("/org/freedesktop/hostname1")
	iface   = "org.freedesktop.hostname1"

	usecInfinity = math.MaxUint64
)

type server struct {
	mu     sync.Mutex
	svc    *dbusservice.Service
	pk     *polkit.Checker
	source string // HostnameSource once this daemon has set the kernel name
}

func newServer(conn *dbus.Conn, pk *polkit.Checker) *server {
	s := &server{pk: pk}
	s.svc = &dbusservice.Service{
		Name: "slinit-hostnamed", Conn: conn, Path: objPath, Iface: iface,
		Props: properties, Get: s.property,
	}
	return s
}

var properties = []dbusservice.Property{
	{Name: "Hostname", Sig: "s", Emits: true},
	{Name: "StaticHostname", Sig: "s", Emits: true},
	{Name: "PrettyHostname", Sig: "s", Emits: true},
	{Name: "DefaultHostname", Sig: "s"},
	{Name: "HostnameSource", Sig: "s", Emits: true},
	{Name: "IconName", Sig: "s", Emits: true},
	{Name: "Chassis", Sig: "s", Emits: true},
	{Name: "Deployment", Sig: "s", Emits: true},
	{Name: "Location", Sig: "s", Emits: true},
	{Name: "Tags", Sig: "as", Emits: true},
	{Name: "KernelName", Sig: "s"},
	{Name: "KernelRelease", Sig: "s"},
	{Name: "KernelVersion", Sig: "s"},
	{Name: "OperatingSystemPrettyName", Sig: "s"},
	{Name: "OperatingSystemFancyName", Sig: "s"},
	{Name: "OperatingSystemCPEName", Sig: "s"},
	{Name: "OperatingSystemSupportEnd", Sig: "t"},
	{Name: "HomeURL", Sig: "s"},
	{Name: "OperatingSystemImageID", Sig: "s"},
	{Name: "OperatingSystemImageVersion", Sig: "s"},
	{Name: "HardwareVendor", Sig: "s"},
	{Name: "HardwareModel", Sig: "s"},
	{Name: "HardwareSKU", Sig: "s"},
	{Name: "HardwareVersion", Sig: "s"},
	{Name: "FirmwareVersion", Sig: "s"},
	{Name: "FirmwareVendor", Sig: "s"},
	{Name: "FirmwareDate", Sig: "t"},
	{Name: "MachineID", Sig: "ay"},
	{Name: "BootID", Sig: "ay"},
	{Name: "VSockCID", Sig: "u"},
	{Name: "ChassisAssetTag", Sig: "s"},
}

// miGet reads one /etc/machine-info field; "" when unset or unreadable.
func miGet(key string) string {
	mi, err := hostname.LoadMachineInfo()
	if err != nil {
		return ""
	}
	return mi.Get(key)
}

func osRelease() map[string]string {
	m, err := hostname.ParseOSRelease(hostname.OSReleasePath)
	if err != nil {
		return map[string]string{}
	}
	return m
}

func chassis() string {
	if v := miGet("CHASSIS"); v != "" {
		return v
	}
	return hostname.DetectChassis()
}

// iconName is ICON_NAME, else "computer-<chassis>", else "computer".
func iconName() string {
	if v := miGet("ICON_NAME"); v != "" {
		return v
	}
	if c := chassis(); c != "" {
		return "computer-" + c
	}
	return "computer"
}

func supportEnd() uint64 {
	t, err := time.Parse("2006-01-02", osRelease()["SUPPORT_END"])
	if err != nil {
		return usecInfinity
	}
	return uint64(t.UnixMicro())
}

func firmwareDate() uint64 {
	if d := hostname.FirmwareDate(); d != 0 {
		return d
	}
	return usecInfinity
}

// hostnameSource is what set the kernel's name: "static" when it is
// /etc/hostname's, "default" when it is the default one, else "transient".
func (s *server) hostnameSource() string {
	s.mu.Lock()
	src := s.source
	s.mu.Unlock()
	if src != "" {
		return src
	}
	k := hostname.KernelHostname()
	if static := hostname.ReadStatic(); static != "" && k == static {
		return "static"
	}
	if k == hostname.DefaultHostname() {
		return "default"
	}
	return "transient"
}

func (s *server) property(name string) (dbus.Variant, bool) {
	mi := func(k string) dbus.Variant { return dbus.MakeVariant(miGet(k)) }
	os := func(k string) dbus.Variant { return dbus.MakeVariant(osRelease()[k]) }
	orDMI := func(k, path string) dbus.Variant {
		if v := miGet(k); v != "" {
			return dbus.MakeVariant(v)
		}
		return dbus.MakeVariant(hostname.DMI(path))
	}
	kname, krel, kver := hostname.Uname()
	switch name {
	case "Hostname":
		if h := hostname.KernelHostname(); h != "" {
			return dbus.MakeVariant(h), true
		}
		return dbus.MakeVariant(hostname.DefaultHostname()), true
	case "StaticHostname":
		return dbus.MakeVariant(hostname.ReadStatic()), true
	case "PrettyHostname":
		return mi("PRETTY_HOSTNAME"), true
	case "DefaultHostname":
		return dbus.MakeVariant(hostname.DefaultHostname()), true
	case "HostnameSource":
		return dbus.MakeVariant(s.hostnameSource()), true
	case "IconName":
		return dbus.MakeVariant(iconName()), true
	case "Chassis":
		return dbus.MakeVariant(chassis()), true
	case "Deployment":
		return mi("DEPLOYMENT"), true
	case "Location":
		return mi("LOCATION"), true
	case "Tags":
		return dbus.MakeVariant(hostname.ParseTags(miGet("TAGS"))), true
	case "KernelName":
		return dbus.MakeVariant(kname), true
	case "KernelRelease":
		return dbus.MakeVariant(krel), true
	case "KernelVersion":
		return dbus.MakeVariant(kver), true
	case "OperatingSystemPrettyName":
		return os("PRETTY_NAME"), true
	case "OperatingSystemFancyName":
		return os("FANCY_NAME"), true
	case "OperatingSystemCPEName":
		return os("CPE_NAME"), true
	case "OperatingSystemSupportEnd":
		return dbus.MakeVariant(supportEnd()), true
	case "HomeURL":
		return os("HOME_URL"), true
	case "OperatingSystemImageID":
		return os("IMAGE_ID"), true
	case "OperatingSystemImageVersion":
		return os("IMAGE_VERSION"), true
	case "HardwareVendor":
		return orDMI("HARDWARE_VENDOR", hostname.DMISysVendorPath), true
	case "HardwareModel":
		return orDMI("HARDWARE_MODEL", hostname.DMIProductNamePath), true
	case "HardwareSKU":
		return orDMI("HARDWARE_SKU", hostname.DMIProductSKUPath), true
	case "HardwareVersion":
		return orDMI("HARDWARE_VERSION", hostname.DMIProductVersionPath), true
	case "FirmwareVersion":
		return dbus.MakeVariant(hostname.DMI(hostname.DMIBiosVersionPath)), true
	case "FirmwareVendor":
		return dbus.MakeVariant(hostname.DMI(hostname.DMIBiosVendorPath)), true
	case "FirmwareDate":
		return dbus.MakeVariant(firmwareDate()), true
	case "MachineID":
		return dbus.MakeVariant(hostname.IDBytes(hostname.MachineIDPath)), true
	case "BootID":
		return dbus.MakeVariant(hostname.IDBytes(hostname.BootIDPath)), true
	case "VSockCID":
		return dbus.MakeVariant(hostname.VSockCID()), true
	case "ChassisAssetTag":
		return dbus.MakeVariant(hostname.DMI(hostname.DMIChassisAssetTagPath)), true
	}
	return dbus.Variant{}, false
}

// updateKernel sets the kernel's hostname the way systemd-hostnamed
// decides it: /etc/hostname first, then the transient name, then the
// default. It reports whether the name or its source changed. Called with
// s.mu held.
func (s *server) updateKernel(transient string) (bool, error) {
	var hn, src string
	if static := hostname.ReadStatic(); static != "" && hostname.Valid(static) {
		hn, src = static, "static"
	} else if transient != "" {
		hn, src = transient, "transient"
	} else {
		hn, src = hostname.DefaultHostname(), "default"
	}
	changed, err := hostname.SetKernelHostname(hn)
	if err != nil {
		return false, err
	}
	if s.source != src {
		s.source = src
		changed = true
	}
	if changed {
		s.svc.Logf("Hostname set to <%s> (%s)", hn, src)
	}
	return changed, nil
}

func fileError(path string, err error) *dbus.Error {
	switch {
	case errors.Is(err, os.ErrPermission):
		return dbus.NewError(iface+".FileIsProtected", []any{"Not allowed to update " + path + "."})
	case errors.Is(err, syscall.EROFS):
		return dbus.NewError(iface+".ReadOnlyFilesystem", []any{path + " is in a read-only filesystem."})
	}
	return dbusservice.Failed("Failed to update %s: %v", path, err)
}

// SetHostname sets the transient hostname, which the kernel uses only
// while /etc/hostname names none.
func (s *server) SetHostname(sender dbus.Sender, name string, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	if name != "" && !hostname.Valid(name) {
		return dbusservice.InvalidArgs("Invalid hostname '%s'", name)
	}
	if err := s.pk.Authorize(sender, iface+".set-hostname", nil, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	changed, err := s.updateKernel(name)
	s.mu.Unlock()
	if err != nil {
		return dbusservice.Failed("Failed to set hostname: %v", err)
	}
	if changed {
		s.svc.Emit("Hostname", "HostnameSource")
	}
	return nil
}

// SetStaticHostname writes /etc/hostname (an empty name removes it) and
// applies it to the kernel.
func (s *server) SetStaticHostname(sender dbus.Sender, name string, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	if name == hostname.ReadStatic() {
		return nil
	}
	if name != "" && !hostname.Valid(name) {
		return dbusservice.InvalidArgs("Invalid hostname '%s'", name)
	}
	if err := s.pk.Authorize(sender, iface+".set-static-hostname", nil, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := hostname.WriteStatic(name); err != nil {
		return fileError(hostname.HostnamePath, err)
	}
	if _, err := s.updateKernel(""); err != nil {
		return dbusservice.Failed("Failed to set hostname: %v", err)
	}
	s.svc.Logf("Changed static hostname to '%s'", name)
	s.svc.Emit("StaticHostname", "Hostname", "HostnameSource")
	return nil
}

// setMachineInfo changes one /etc/machine-info field. The pretty hostname
// uses the static hostname's action, since the two change together.
func (s *server) setMachineInfo(sender dbus.Sender, key, prop, value string, valid func(string) bool, interactive bool) *dbus.Error {
	defer s.svc.Touch()()
	if value == miGet(key) {
		return nil
	}
	if value != "" && !valid(value) {
		return dbusservice.InvalidArgs("Invalid %s '%s'", strings.ToLower(strings.ReplaceAll(key, "_", " ")), value)
	}
	action := iface + ".set-machine-info"
	if key == "PRETTY_HOSTNAME" {
		action = iface + ".set-static-hostname"
	}
	if err := s.pk.Authorize(sender, action, nil, interactive); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mi, err := hostname.LoadMachineInfo()
	if err != nil {
		return dbusservice.Failed("Failed to read machine info: %v", err)
	}
	mi.Set(key, value)
	if err := mi.Save(); err != nil {
		return fileError(hostname.MachineInfoPath, err)
	}
	s.svc.Logf("Changed %s to '%s'", strings.ToLower(strings.ReplaceAll(key, "_", " ")), value)
	s.svc.Emit(prop)
	return nil
}

func safe(v string) bool       { return hostname.SafeString(v, false) }
func safeStrict(v string) bool { return hostname.SafeString(v, true) }

func (s *server) SetPrettyHostname(sender dbus.Sender, name string, interactive bool) *dbus.Error {
	return s.setMachineInfo(sender, "PRETTY_HOSTNAME", "PrettyHostname", name, safe, interactive)
}

func (s *server) SetIconName(sender dbus.Sender, name string, interactive bool) *dbus.Error {
	return s.setMachineInfo(sender, "ICON_NAME", "IconName", name, safeStrict, interactive)
}

func (s *server) SetChassis(sender dbus.Sender, name string, interactive bool) *dbus.Error {
	return s.setMachineInfo(sender, "CHASSIS", "Chassis", name, hostname.ValidChassis, interactive)
}

func (s *server) SetDeployment(sender dbus.Sender, name string, interactive bool) *dbus.Error {
	return s.setMachineInfo(sender, "DEPLOYMENT", "Deployment", name, hostname.ValidDeployment, interactive)
}

func (s *server) SetLocation(sender dbus.Sender, name string, interactive bool) *dbus.Error {
	return s.setMachineInfo(sender, "LOCATION", "Location", name, safe, interactive)
}

// writeTags stores the tag list as TAGS= (an empty list removes it).
func (s *server) writeTags(sender dbus.Sender, tags []string) *dbus.Error {
	if err := s.pk.Authorize(sender, iface+".set-machine-info", nil, false); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mi, err := hostname.LoadMachineInfo()
	if err != nil {
		return dbusservice.Failed("Failed to read machine info: %v", err)
	}
	mi.Set("TAGS", strings.Join(tags, ":"))
	if err := mi.Save(); err != nil {
		return fileError(hostname.MachineInfoPath, err)
	}
	s.svc.Logf("Changed tags to '%s'", strings.Join(tags, ":"))
	s.svc.Emit("Tags")
	return nil
}

// SetTags replaces the machine's tags.
func (s *server) SetTags(sender dbus.Sender, tags []string) *dbus.Error {
	defer s.svc.Touch()()
	if len(tags) > hostname.MaxTags {
		return dbusservice.InvalidArgs("Too many machine tags specified.")
	}
	norm, err := hostname.NormalizeTags(tags)
	if err != nil {
		return dbusservice.InvalidArgs("Invalid tags '%s'", strings.Join(tags, ":"))
	}
	if strings.Join(norm, ":") == strings.Join(hostname.ParseTags(miGet("TAGS")), ":") {
		return nil
	}
	return s.writeTags(sender, norm)
}

// AddAndRemoveTags removes the tags whose key matches a pattern in remove,
// then adds the tags in add (replacing a tag with the same key).
func (s *server) AddAndRemoveTags(sender dbus.Sender, add, remove []string) *dbus.Error {
	defer s.svc.Touch()()
	if _, err := hostname.NormalizeTags(add); err != nil {
		return dbusservice.InvalidArgs("Invalid tags to add '%s'", strings.Join(add, ":"))
	}
	if len(remove) > hostname.MaxTags {
		return dbusservice.InvalidArgs("Too many tags patterns to remove.")
	}
	cur := hostname.ParseTags(miGet("TAGS"))
	next, err := hostname.NormalizeTags(append(hostname.RemoveTags(cur, remove), add...))
	if err != nil {
		return dbusservice.InvalidArgs("Too many machine tags specified.")
	}
	if strings.Join(next, ":") == strings.Join(cur, ":") {
		return nil
	}
	return s.writeTags(sender, next)
}

// GetProductUUID returns the firmware's product UUID.
func (s *server) GetProductUUID(sender dbus.Sender, interactive bool) ([]byte, *dbus.Error) {
	defer s.svc.Touch()()
	if err := s.pk.Authorize(sender, iface+".get-product-uuid", nil, interactive); err != nil {
		return nil, err
	}
	u, err := hostname.ProductUUID()
	if err != nil {
		return nil, dbus.NewError(iface+".NoProductUUID", []any{"Failed to read product UUID from firmware."})
	}
	return u, nil
}

// GetHardwareSerial returns the firmware's serial number.
func (s *server) GetHardwareSerial(sender dbus.Sender) (string, *dbus.Error) {
	defer s.svc.Touch()()
	if err := s.pk.Authorize(sender, iface+".get-hardware-serial", nil, false); err != nil {
		return "", err
	}
	v, err := hostname.HardwareSerial()
	if err != nil {
		return "", dbus.NewError(iface+".NoHardwareSerial", []any{"Failed to read hardware serial from firmware."})
	}
	return v, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// GetMachineInfo returns one raw /etc/machine-info field.
func (s *server) GetMachineInfo(field string) (string, *dbus.Error) {
	defer s.svc.Touch()()
	if field == "" {
		return "", dbusservice.InvalidArgs("Field name must not be empty.")
	}
	if !envName.MatchString(field) {
		return "", dbusservice.InvalidArgs("Invalid field name '%s'.", field)
	}
	if v := miGet(field); v != "" {
		return v, nil
	}
	return "", dbus.NewError(iface+".FieldNotSet", []any{"Field '" + field + "' is not set or empty in /etc/machine-info."})
}

// Describe returns everything as one JSON object. The serial number and
// product UUID are included only for a caller polkit lets read them; the
// rest is public, so a refusal does not fail the call.
func (s *server) Describe(sender dbus.Sender) (string, *dbus.Error) {
	defer s.svc.Touch()()
	privileged := s.pk.Authorize(sender, iface+".get-description", nil, false) == nil
	d := map[string]any{}
	for _, p := range properties {
		v, _ := s.property(p.Name)
		d[p.Name] = v.Value()
	}
	for _, k := range []string{"MachineID", "BootID"} {
		if b, ok := d[k].([]byte); ok && len(b) == 16 {
			d[k] = hex.EncodeToString(b)
		} else {
			d[k] = nil
		}
	}
	for _, k := range []string{"OperatingSystemSupportEnd", "FirmwareDate"} {
		if d[k] == uint64(usecInfinity) {
			d[k] = nil
		}
	}
	d["OperatingSystemHomeURL"] = d["HomeURL"]
	delete(d, "HomeURL")
	d["MachineTags"] = d["Tags"]
	delete(d, "Tags")
	d["OperatingSystemReleaseData"] = envPairs(osRelease())
	if mi, err := hostname.LoadMachineInfo(); err == nil && mi != nil {
		d["MachineInformationData"] = mi.Pairs()
	}
	if privileged {
		if v, err := hostname.HardwareSerial(); err == nil {
			d["HardwareSerial"] = v
		}
		if u, err := hostname.ProductUUID(); err == nil {
			d["ProductUUID"] = hex.EncodeToString(u)
		}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", dbusservice.Failed("Failed to format JSON data: %v", err)
	}
	return string(b), nil
}

func envPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
