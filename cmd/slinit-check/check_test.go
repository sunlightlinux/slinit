package main

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestCheckNamespaceConfigClean(t *testing.T) {
	desc := &config.ServiceDescription{Type: service.TypeProcess}
	e, w := checkNamespaceConfig(desc, "test")
	if e != 0 || w != 0 {
		t.Errorf("clean config: errors=%d warnings=%d, want 0/0", e, w)
	}
}

func TestCheckNamespaceConfigUidMapWithoutUser(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:            service.TypeProcess,
		NamespaceUidMap: []config.IDMapping{{ContainerID: 0, HostID: 1000, Size: 1}},
	}
	e, _ := checkNamespaceConfig(desc, "test")
	if e != 1 {
		t.Errorf("uid-map without namespace-user: errors=%d, want 1", e)
	}
}

func TestCheckNamespaceConfigGidMapWithoutUser(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:            service.TypeProcess,
		NamespaceGidMap: []config.IDMapping{{ContainerID: 0, HostID: 1000, Size: 1}},
	}
	e, _ := checkNamespaceConfig(desc, "test")
	if e != 1 {
		t.Errorf("gid-map without namespace-user: errors=%d, want 1", e)
	}
}

func TestCheckNamespaceConfigUserWithoutMaps(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:          service.TypeProcess,
		NamespaceUser: true,
	}
	_, w := checkNamespaceConfig(desc, "test")
	if w != 2 {
		t.Errorf("namespace-user without maps: warnings=%d, want 2", w)
	}
}

func TestCheckNamespaceConfigUserWithMaps(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:            service.TypeProcess,
		NamespaceUser:   true,
		NamespaceUidMap: []config.IDMapping{{ContainerID: 0, HostID: 1000, Size: 65536}},
		NamespaceGidMap: []config.IDMapping{{ContainerID: 0, HostID: 1000, Size: 65536}},
	}
	e, w := checkNamespaceConfig(desc, "test")
	if e != 0 || w != 0 {
		t.Errorf("valid user ns config: errors=%d warnings=%d, want 0/0", e, w)
	}
}

func TestCheckNamespaceConfigPidWithoutMount(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:         service.TypeProcess,
		NamespacePID: true,
	}
	_, w := checkNamespaceConfig(desc, "test")
	if w < 1 {
		t.Errorf("pid-ns without mount-ns: warnings=%d, want >=1", w)
	}
}

func TestCheckNamespaceConfigPidWithMount(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:           service.TypeProcess,
		NamespacePID:   true,
		NamespaceMount: true,
	}
	e, w := checkNamespaceConfig(desc, "test")
	if e != 0 || w != 0 {
		t.Errorf("pid+mount ns: errors=%d warnings=%d, want 0/0", e, w)
	}
}

func TestCheckNamespaceConfigOnInternalService(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:         service.TypeInternal,
		NamespacePID: true,
	}
	_, w := checkNamespaceConfig(desc, "test")
	if w < 1 {
		t.Errorf("ns on internal: warnings=%d, want >=1", w)
	}
}

func TestCheckNamespaceConfigOnTriggeredService(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:           service.TypeTriggered,
		NamespaceMount: true,
	}
	_, w := checkNamespaceConfig(desc, "test")
	if w < 1 {
		t.Errorf("ns on triggered: warnings=%d, want >=1", w)
	}
}

func TestFindMappingOverlapNone(t *testing.T) {
	maps := []config.IDMapping{
		{ContainerID: 0, HostID: 1000, Size: 100},
		{ContainerID: 100, HostID: 2000, Size: 100},
	}
	if o := findMappingOverlap(maps); o != "" {
		t.Errorf("non-overlapping maps reported overlap: %s", o)
	}
}

func TestFindMappingOverlapDetected(t *testing.T) {
	maps := []config.IDMapping{
		{ContainerID: 0, HostID: 1000, Size: 100},
		{ContainerID: 50, HostID: 2000, Size: 100},
	}
	if o := findMappingOverlap(maps); o == "" {
		t.Error("overlapping maps not detected")
	}
}

func TestFindMappingOverlapAdjacent(t *testing.T) {
	maps := []config.IDMapping{
		{ContainerID: 0, HostID: 1000, Size: 100},
		{ContainerID: 100, HostID: 2000, Size: 50},
	}
	if o := findMappingOverlap(maps); o != "" {
		t.Errorf("adjacent (non-overlapping) maps reported overlap: %s", o)
	}
}

func TestCheckNamespaceOverlappingUidMaps(t *testing.T) {
	desc := &config.ServiceDescription{
		Type:          service.TypeProcess,
		NamespaceUser: true,
		NamespaceUidMap: []config.IDMapping{
			{ContainerID: 0, HostID: 1000, Size: 65536},
			{ContainerID: 100, HostID: 2000, Size: 100},
		},
		NamespaceGidMap: []config.IDMapping{
			{ContainerID: 0, HostID: 1000, Size: 65536},
		},
	}
	e, _ := checkNamespaceConfig(desc, "test")
	if e != 1 {
		t.Errorf("overlapping uid maps: errors=%d, want 1", e)
	}
}
