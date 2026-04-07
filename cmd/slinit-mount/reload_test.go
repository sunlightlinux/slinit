package main

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/autofs"
)

func TestMountUnitKey(t *testing.T) {
	mu := &autofs.MountUnit{Name: "data", Where: "/mnt/data", What: "/dev/sda1", Type: "ext4"}
	key := mountUnitKey(mu)
	if key != "/mnt/data" {
		t.Errorf("mountUnitKey = %q, want %q", key, "/mnt/data")
	}
}

func TestMountUnitChangedIdentical(t *testing.T) {
	a := &autofs.MountUnit{
		Name: "data", What: "/dev/sda1", Where: "/mnt/data",
		Type: "ext4", Options: "rw,noatime", Timeout: 300 * time.Second,
		AutofsType: "indirect", DirMode: 0755,
	}
	b := &autofs.MountUnit{
		Name: "data-renamed", What: "/dev/sda1", Where: "/mnt/data",
		Type: "ext4", Options: "rw,noatime", Timeout: 300 * time.Second,
		AutofsType: "indirect", DirMode: 0755,
	}
	if mountUnitChanged(a, b) {
		t.Error("identical units should not be reported as changed (Name is ignored)")
	}
}

func TestMountUnitChangedWhat(t *testing.T) {
	a := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4"}
	b := &autofs.MountUnit{What: "/dev/sdb1", Type: "ext4"}
	if !mountUnitChanged(a, b) {
		t.Error("different What should be reported as changed")
	}
}

func TestMountUnitChangedType(t *testing.T) {
	a := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4"}
	b := &autofs.MountUnit{What: "/dev/sda1", Type: "xfs"}
	if !mountUnitChanged(a, b) {
		t.Error("different Type should be reported as changed")
	}
}

func TestMountUnitChangedOptions(t *testing.T) {
	a := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4", Options: "rw"}
	b := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4", Options: "ro"}
	if !mountUnitChanged(a, b) {
		t.Error("different Options should be reported as changed")
	}
}

func TestMountUnitChangedTimeout(t *testing.T) {
	a := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4", Timeout: 60 * time.Second}
	b := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4", Timeout: 120 * time.Second}
	if !mountUnitChanged(a, b) {
		t.Error("different Timeout should be reported as changed")
	}
}

func TestMountUnitChangedAutofsType(t *testing.T) {
	a := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4", AutofsType: "indirect"}
	b := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4", AutofsType: "direct"}
	if !mountUnitChanged(a, b) {
		t.Error("different AutofsType should be reported as changed")
	}
}

func TestMountUnitChangedDirMode(t *testing.T) {
	a := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4", DirMode: 0755}
	b := &autofs.MountUnit{What: "/dev/sda1", Type: "ext4", DirMode: 0700}
	if !mountUnitChanged(a, b) {
		t.Error("different DirMode should be reported as changed")
	}
}
