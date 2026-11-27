package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestSystemdDependencyMapping(t *testing.T) {
	tests := []struct {
		name                     string
		unit                     string
		depends, waits           []string
		after, before            []string
		asserts                  []string
		noteContains, notWarnFor string
	}{
		{
			name:  "After= orders only",
			unit:  "[Unit]\nAfter=db.service\n",
			after: []string{"db"},
		},
		{
			name:   "Before= orders only",
			unit:   "[Unit]\nBefore=web.service\n",
			before: []string{"web"},
		},
		{
			name:  "Wants= with After= is one waits-for, no extra after:",
			unit:  "[Unit]\nWants=db.service\nAfter=db.service cache.service\n",
			waits: []string{"db"},
			after: []string{"cache"},
		},
		{
			name:    "BindsTo= is depends-on",
			unit:    "[Unit]\nBindsTo=dev.service\nAfter=dev.service\n",
			depends: []string{"dev"},
		},
		{
			name:    "BindTo= is the old spelling of BindsTo=",
			unit:    "[Unit]\nBindTo=dev.service\n",
			depends: []string{"dev"},
		},
		{
			name:    "RequiresOverridable= reads as Requires=",
			unit:    "[Unit]\nRequiresOverridable=db.service\n",
			depends: []string{"db"},
		},
		{
			name:    "RequisiteOverridable= reads as Requisite=",
			unit:    "[Unit]\nRequisiteOverridable=db.service\n",
			asserts: []string{"db"},
		},
		{
			name:  "a target in After= is dropped, the service kept",
			unit:  "[Unit]\nAfter=network.target db.service\n",
			after: []string{"db"},
		},
		{
			name:         "PartOf= has no equivalent and says so",
			unit:         "[Unit]\nPartOf=db.service\n",
			noteContains: "PartOf=db.service has no slinit equivalent",
		},
		{
			name:         "StopWhenUnneeded=yes explains slinit's default",
			unit:         "[Unit]\nStopWhenUnneeded=yes\n",
			noteContains: "started only as a dependency",
		},
		{
			name:       "StopWhenUnneeded=no restates the default: no note",
			unit:       "[Unit]\nStopWhenUnneeded=no\n",
			notWarnFor: "StopWhenUnneeded",
		},
		{
			name:       "OnFailureJobMode= is systemd-internal: no note",
			unit:       "[Unit]\nOnFailureJobMode=replace\n",
			notWarnFor: "OnFailureJobMode",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			desc, warns := convertWarns(t, tc.unit+"[Service]\nExecStart=/bin/true\n")
			eq := func(what string, got, want []string) {
				if len(got) == 0 && len(want) == 0 {
					return
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s = %q, want %q", what, got, want)
				}
			}
			eq("depends-on", desc.DependsOn, tc.depends)
			eq("waits-for", desc.WaitsFor, tc.waits)
			eq("after", desc.After, tc.after)
			eq("before", desc.Before, tc.before)
			var asserts []string
			for _, p := range desc.Predicates {
				if strings.Contains(p.String(), "service-started") {
					asserts = append(asserts, p.Param)
				}
			}
			eq("assert-service-started", asserts, tc.asserts)
			all := ""
			for _, w := range warns {
				all += w.Level + ": " + w.Msg + "\n"
			}
			if tc.noteContains != "" && !strings.Contains(all, tc.noteContains) {
				t.Errorf("no warning containing %q in:\n%s", tc.noteContains, all)
			}
			if tc.notWarnFor != "" && strings.Contains(all, tc.notWarnFor) {
				t.Errorf("unexpected warning about %s:\n%s", tc.notWarnFor, all)
			}
		})
	}
}

// A unit loaded directly with Before= naming a service that is not
// installed still loads, as in systemd, and one that is installed gets
// the ordering edge.
func TestSystemdBeforeOnMissingUnitLoads(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "early.service", `[Unit]
Before=late.service gone.service
[Service]
ExecStart=/usr/bin/early
`)
	writeUnit(t, units, "late.service", "[Service]\nExecStart=/usr/bin/late\n")
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, level+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })

	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	ss.SetLoader(dl)
	svc, err := dl.LoadService("early")
	if err != nil {
		t.Fatalf("unit with Before= on a missing unit does not load: %v", err)
	}
	var before bool
	for _, d := range svc.Record().Dependencies() {
		if d.To.Name() == "late" && d.DepType == service.DepBefore {
			before = true
		}
		if d.To.Name() == "gone" {
			t.Errorf("an edge to the missing unit was kept")
		}
	}
	if !before {
		t.Errorf("no before: edge to late; dependencies: %v", svc.Record().Dependencies())
	}
	found := false
	for _, g := range got {
		found = found || (strings.HasPrefix(g, "NOTE") && strings.Contains(g, "before: gone"))
	}
	if !found {
		t.Errorf("no NOTE that gone was left out: %v", got)
	}
}
