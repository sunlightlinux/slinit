package platform

import (
	"os"
	"testing"
)

// mockFS provides fake file contents and stat results for testing.
type mockFS struct {
	files map[string][]byte // path → content
	dirs  map[string]bool   // paths that are directories
}

func newMockFS() *mockFS {
	return &mockFS{
		files: make(map[string][]byte),
		dirs:  make(map[string]bool),
	}
}

func (m *mockFS) readFile(path string) ([]byte, error) {
	if data, ok := m.files[path]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockFS) stat(path string) (os.FileInfo, error) {
	if m.dirs[path] {
		return fakeDirInfo{}, nil
	}
	if _, ok := m.files[path]; ok {
		return fakeFileInfo{}, nil
	}
	return nil, os.ErrNotExist
}

type fakeFileInfo struct{ os.FileInfo }

func (fakeFileInfo) IsDir() bool { return false }

type fakeDirInfo struct{ os.FileInfo }

func (fakeDirInfo) IsDir() bool { return true }

func withMock(m *mockFS, fn func()) {
	origRead := readFileFunc
	origStat := statFunc
	readFileFunc = m.readFile
	statFunc = m.stat
	defer func() {
		readFileFunc = origRead
		statFunc = origStat
	}()
	fn()
}

func TestDetectDocker(t *testing.T) {
	m := newMockFS()
	m.files["/.dockerenv"] = []byte{}
	withMock(m, func() {
		if got := Detect(); got != Docker {
			t.Errorf("expected Docker, got %q", got)
		}
	})
}

func TestDetectDockerViaEnviron(t *testing.T) {
	m := newMockFS()
	m.files["/proc/1/environ"] = []byte("PATH=/usr/bin\x00container=docker\x00TERM=xterm")
	withMock(m, func() {
		if got := Detect(); got != Docker {
			t.Errorf("expected Docker, got %q", got)
		}
	})
}

func TestDetectPodman(t *testing.T) {
	m := newMockFS()
	m.files["/run/.containerenv"] = []byte{}
	withMock(m, func() {
		if got := Detect(); got != Podman {
			t.Errorf("expected Podman, got %q", got)
		}
	})
}

func TestDetectLXC(t *testing.T) {
	m := newMockFS()
	m.files["/proc/1/environ"] = []byte("container=lxc\x00")
	withMock(m, func() {
		if got := Detect(); got != LXC {
			t.Errorf("expected LXC, got %q", got)
		}
	})
}

func TestDetectSystemdNspawn(t *testing.T) {
	m := newMockFS()
	m.files["/proc/1/environ"] = []byte("container=systemd-nspawn\x00")
	withMock(m, func() {
		if got := Detect(); got != SystemdNspawn {
			t.Errorf("expected SystemdNspawn, got %q", got)
		}
	})
}

func TestDetectWSLViaOsrelease(t *testing.T) {
	m := newMockFS()
	m.files["/proc/sys/kernel/osrelease"] = []byte("5.15.90.1-Microsoft-standard-WSL2\n")
	withMock(m, func() {
		if got := Detect(); got != WSL {
			t.Errorf("expected WSL, got %q", got)
		}
	})
}

func TestDetectWSLViaBinfmt(t *testing.T) {
	m := newMockFS()
	m.files["/proc/sys/fs/binfmt_misc/WSLInterop"] = []byte{}
	withMock(m, func() {
		if got := Detect(); got != WSL {
			t.Errorf("expected WSL, got %q", got)
		}
	})
}

func TestDetectUML(t *testing.T) {
	m := newMockFS()
	m.files["/proc/cpuinfo"] = []byte("vendor_id: UML\nmodel name: UML")
	withMock(m, func() {
		if got := Detect(); got != UML {
			t.Errorf("expected UML, got %q", got)
		}
	})
}

func TestDetectVserver(t *testing.T) {
	m := newMockFS()
	m.files["/proc/self/status"] = []byte("Name:\tinit\nVxID:\t42\n")
	withMock(m, func() {
		if got := Detect(); got != Vserver {
			t.Errorf("expected Vserver, got %q", got)
		}
	})
}

func TestDetectOpenVZ(t *testing.T) {
	m := newMockFS()
	m.files["/proc/vz/veinfo"] = []byte{}
	// /proc/vz/version must NOT exist for OpenVZ guest
	withMock(m, func() {
		if got := Detect(); got != OpenVZ {
			t.Errorf("expected OpenVZ, got %q", got)
		}
	})
}

func TestDetectXenDom0(t *testing.T) {
	m := newMockFS()
	m.dirs["/proc/xen"] = true
	m.files["/proc/xen/capabilities"] = []byte("control_d")
	withMock(m, func() {
		if got := Detect(); got != Xen0 {
			t.Errorf("expected Xen0, got %q", got)
		}
	})
}

func TestDetectXenDomU(t *testing.T) {
	m := newMockFS()
	m.dirs["/proc/xen"] = true
	m.files["/proc/xen/capabilities"] = []byte("") // no control_d
	withMock(m, func() {
		if got := Detect(); got != XenU {
			t.Errorf("expected XenU, got %q", got)
		}
	})
}

func TestDetectNone(t *testing.T) {
	m := newMockFS()
	withMock(m, func() {
		if got := Detect(); got != None {
			t.Errorf("expected None, got %q", got)
		}
	})
}

func TestDetectRKT(t *testing.T) {
	m := newMockFS()
	m.files["/proc/1/environ"] = []byte("container=rkt\x00")
	withMock(m, func() {
		if got := Detect(); got != RKT {
			t.Errorf("expected RKT, got %q", got)
		}
	})
}

func TestMatchesKeyword(t *testing.T) {
	tests := []struct {
		keyword  string
		platform Type
		want     bool
	}{
		{"-docker", Docker, true},
		{"-lxc", LXC, true},
		{"-podman", Podman, true},
		{"-wsl", WSL, true},
		{"-xen0", Xen0, true},
		{"-xenu", XenU, true},
		{"-systemd-nspawn", SystemdNspawn, true},
		{"-docker", LXC, false},
		{"-lxc", Docker, false},
		{"-docker", None, false},
		{"nodcker", Docker, false}, // typo should not match
		{"nodocker", Docker, true}, // "noplatform" form
		{"-Docker", Docker, true},  // case insensitive
		{"-DOCKER", Docker, true},  // case insensitive
	}
	for _, tc := range tests {
		t.Run(tc.keyword+"_"+string(tc.platform), func(t *testing.T) {
			if got := MatchesKeyword(tc.keyword, tc.platform); got != tc.want {
				t.Errorf("MatchesKeyword(%q, %q) = %v, want %v", tc.keyword, tc.platform, got, tc.want)
			}
		})
	}
}

func TestShouldSkip(t *testing.T) {
	// Service with -docker -lxc keywords on Docker platform → skip
	if !ShouldSkip([]string{"-docker", "-lxc"}, Docker) {
		t.Error("expected skip on Docker with -docker keyword")
	}
	// Same keywords on bare metal → don't skip
	if ShouldSkip([]string{"-docker", "-lxc"}, None) {
		t.Error("should not skip on bare metal")
	}
	// No keywords → don't skip
	if ShouldSkip(nil, Docker) {
		t.Error("should not skip with no keywords")
	}
	// Unrelated keyword → don't skip
	if ShouldSkip([]string{"-xen0"}, Docker) {
		t.Error("should not skip Docker with -xen0 keyword")
	}
}

func TestIsValid(t *testing.T) {
	for _, pt := range AllTypes() {
		if !IsValid(string(pt)) {
			t.Errorf("%q should be valid", pt)
		}
	}
	if !IsValid("") {
		t.Error("empty string should be valid (none)")
	}
	if !IsValid("none") {
		t.Error("'none' should be valid")
	}
	if !IsValid("DOCKER") {
		t.Error("'DOCKER' (uppercase) should be valid")
	}
	if IsValid("invalid-platform") {
		t.Error("'invalid-platform' should not be valid")
	}
}
