package autofs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// --- ParseV5Packet tests ---

func TestParseV5PacketValid(t *testing.T) {
	buf := make([]byte, V5PacketSize)
	// Proto version = 5 (little-endian)
	buf[0] = 5
	// Type = PktTypeMissingIndirect (3)
	buf[4] = 3
	// WaitQueueToken = 42
	buf[8] = 42
	// PID = 1234 (0x04D2)
	buf[32] = 0xD2
	buf[33] = 0x04
	// Name = "subdir"
	copy(buf[44:], "subdir")

	pkt, err := ParseV5Packet(buf)
	if err != nil {
		t.Fatalf("ParseV5Packet failed: %v", err)
	}
	if pkt.ProtoVersion != 5 {
		t.Errorf("ProtoVersion = %d, want 5", pkt.ProtoVersion)
	}
	if pkt.Type != PktTypeMissingIndirect {
		t.Errorf("Type = %d, want %d", pkt.Type, PktTypeMissingIndirect)
	}
	if pkt.WaitQueueToken != 42 {
		t.Errorf("WaitQueueToken = %d, want 42", pkt.WaitQueueToken)
	}
	if pkt.PID != 1234 {
		t.Errorf("PID = %d, want 1234", pkt.PID)
	}
	if pkt.NameString() != "subdir" {
		t.Errorf("Name = %q, want %q", pkt.NameString(), "subdir")
	}
}

func TestParseV5PacketTooShort(t *testing.T) {
	buf := make([]byte, 50)
	_, err := ParseV5Packet(buf)
	if err == nil {
		t.Fatal("expected error for short buffer")
	}
}

func TestV5PacketIsMissing(t *testing.T) {
	tests := []struct {
		ptype int32
		want  bool
	}{
		{PktTypeMissing, true},
		{PktTypeMissingIndirect, true},
		{PktTypeMissingDirect, true},
		{PktTypeExpire, false},
		{PktTypeExpireMulti, false},
	}
	for _, tt := range tests {
		pkt := &V5Packet{Type: tt.ptype}
		if got := pkt.IsMissing(); got != tt.want {
			t.Errorf("IsMissing(type=%d) = %v, want %v", tt.ptype, got, tt.want)
		}
	}
}

func TestV5PacketIsExpire(t *testing.T) {
	tests := []struct {
		ptype int32
		want  bool
	}{
		{PktTypeExpire, true},
		{PktTypeExpireMulti, true},
		{PktTypeExpireIndirect, true},
		{PktTypeExpireDirect, true},
		{PktTypeMissing, false},
		{PktTypeMissingDirect, false},
	}
	for _, tt := range tests {
		pkt := &V5Packet{Type: tt.ptype}
		if got := pkt.IsExpire(); got != tt.want {
			t.Errorf("IsExpire(type=%d) = %v, want %v", tt.ptype, got, tt.want)
		}
	}
}

// --- Config parsing tests ---

func TestParseMountUnitFull(t *testing.T) {
	input := `# NFS home directories
description = NFS home directories
what = fileserver:/export/home
where = /home
type = nfs
options = rw,soft,intr
timeout = 300
autofs-type = indirect
directory-mode = 0750
after: network-online
`
	mu, err := ParseMountUnit(strings.NewReader(input), "home")
	if err != nil {
		t.Fatalf("ParseMountUnit failed: %v", err)
	}

	if mu.Name != "home" {
		t.Errorf("Name = %q, want %q", mu.Name, "home")
	}
	if mu.Description != "NFS home directories" {
		t.Errorf("Description = %q", mu.Description)
	}
	if mu.What != "fileserver:/export/home" {
		t.Errorf("What = %q", mu.What)
	}
	if mu.Where != "/home" {
		t.Errorf("Where = %q", mu.Where)
	}
	if mu.Type != "nfs" {
		t.Errorf("Type = %q", mu.Type)
	}
	if mu.Options != "rw,soft,intr" {
		t.Errorf("Options = %q", mu.Options)
	}
	if mu.Timeout.Seconds() != 300 {
		t.Errorf("Timeout = %v, want 300s", mu.Timeout)
	}
	if mu.AutofsType != TypeIndirect {
		t.Errorf("AutofsType = %q", mu.AutofsType)
	}
	if mu.DirMode != 0750 {
		t.Errorf("DirMode = %o, want 0750", mu.DirMode)
	}
	if len(mu.After) != 1 || mu.After[0] != "network-online" {
		t.Errorf("After = %v, want [network-online]", mu.After)
	}
}

func TestParseMountUnitMinimal(t *testing.T) {
	input := `what = /dev/sda1
where = /mnt/data
type = ext4
`
	mu, err := ParseMountUnit(strings.NewReader(input), "data")
	if err != nil {
		t.Fatalf("ParseMountUnit failed: %v", err)
	}
	if mu.AutofsType != TypeIndirect {
		t.Errorf("default AutofsType = %q, want %q", mu.AutofsType, TypeIndirect)
	}
	if mu.DirMode != 0755 {
		t.Errorf("default DirMode = %o, want 0755", mu.DirMode)
	}
}

func TestParseMountUnitInvalidAutofsType(t *testing.T) {
	input := `what = /dev/sda1
where = /mnt
type = ext4
autofs-type = invalid
`
	_, err := ParseMountUnit(strings.NewReader(input), "bad")
	if err == nil {
		t.Fatal("expected error for invalid autofs-type")
	}
}

func TestParseMountUnitUnknownSetting(t *testing.T) {
	input := `what = /dev/sda1
where = /mnt
type = ext4
bogus = value
`
	_, err := ParseMountUnit(strings.NewReader(input), "bad")
	if err == nil {
		t.Fatal("expected error for unknown setting")
	}
}

// --- Validation tests ---

func TestValidateMountUnitValid(t *testing.T) {
	mu := &MountUnit{
		Name:  "test",
		What:  "/dev/sda1",
		Where: "/mnt/test",
		Type:  "ext4",
	}
	if err := ValidateMountUnit(mu); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateMountUnitMissingWhere(t *testing.T) {
	mu := &MountUnit{Name: "test", What: "/dev/sda1", Type: "ext4"}
	if err := ValidateMountUnit(mu); err == nil {
		t.Error("expected error for missing 'where'")
	}
}

func TestValidateMountUnitRelativeWhere(t *testing.T) {
	mu := &MountUnit{Name: "test", What: "/dev/sda1", Where: "mnt/test", Type: "ext4"}
	if err := ValidateMountUnit(mu); err == nil {
		t.Error("expected error for relative 'where'")
	}
}

func TestValidateMountUnitMissingWhat(t *testing.T) {
	mu := &MountUnit{Name: "test", Where: "/mnt/test", Type: "ext4"}
	if err := ValidateMountUnit(mu); err == nil {
		t.Error("expected error for missing 'what'")
	}
}

func TestValidateMountUnitMissingType(t *testing.T) {
	mu := &MountUnit{Name: "test", What: "/dev/sda1", Where: "/mnt/test"}
	if err := ValidateMountUnit(mu); err == nil {
		t.Error("expected error for missing 'type'")
	}
}

// --- LoadMountUnits tests ---

func TestLoadMountUnitsFromDir(t *testing.T) {
	dir := t.TempDir()

	// Write a valid .mount file
	content := `what = /dev/sda1
where = /mnt/data
type = ext4
`
	if err := os.WriteFile(filepath.Join(dir, "data.mount"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Write a non-.mount file (should be ignored)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}

	units, err := LoadMountUnits([]string{dir})
	if err != nil {
		t.Fatalf("LoadMountUnits failed: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("expected 1 unit, got %d", len(units))
	}
	if units[0].Name != "data" {
		t.Errorf("unit name = %q, want %q", units[0].Name, "data")
	}
}

func TestLoadMountUnitsNonExistentDir(t *testing.T) {
	units, err := LoadMountUnits([]string{"/nonexistent/path"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(units) != 0 {
		t.Errorf("expected 0 units, got %d", len(units))
	}
}

// --- parseMountFlags tests ---

func TestParseMountFlagsEmpty(t *testing.T) {
	flags, opts := parseMountFlags("")
	if flags != 0 {
		t.Errorf("flags = %x, want 0", flags)
	}
	if opts != "" {
		t.Errorf("opts = %q, want empty", opts)
	}
}

func TestParseMountFlagsKnown(t *testing.T) {
	flags, opts := parseMountFlags("ro,nosuid,nodev,noexec")
	expected := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if flags != expected {
		t.Errorf("flags = %x, want %x", flags, expected)
	}
	if opts != "" {
		t.Errorf("remaining opts = %q, want empty", opts)
	}
}

func TestParseMountFlagsMixed(t *testing.T) {
	flags, opts := parseMountFlags("rw,nosuid,soft,intr,timeo=30")
	if flags != uintptr(unix.MS_NOSUID) {
		t.Errorf("flags = %x, want MS_NOSUID", flags)
	}
	if opts != "soft,intr,timeo=30" {
		t.Errorf("remaining opts = %q, want %q", opts, "soft,intr,timeo=30")
	}
}

func TestParseMountFlagsBind(t *testing.T) {
	flags, _ := parseMountFlags("bind")
	if flags != uintptr(unix.MS_BIND) {
		t.Errorf("flags = %x, want MS_BIND", flags)
	}

	flags2, _ := parseMountFlags("rbind")
	expected := uintptr(unix.MS_BIND | unix.MS_REC)
	if flags2 != expected {
		t.Errorf("flags = %x, want MS_BIND|MS_REC", flags2)
	}
}
