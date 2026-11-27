package config

import (
	"slices"
	"strings"
	"testing"
)

func TestSystemdDependencyMapping(t *testing.T) {
	tests := []struct {
		name                     string
		unit                     string
		depends, waits           []string
		asserts                  []string
		stopWhenUnneeded         bool
		noteContains, notWarnFor string
	}{
		{
			name:  "After= is still waits-for (see applyUnitKey)",
			unit:  "[Unit]\nAfter=db.service\n",
			waits: []string{"db"},
		},
		{
			name:         "Before= is reported, not mapped",
			unit:         "[Unit]\nBefore=web.service\n",
			noteContains: "Before=web.service is not carried over",
		},
		{
			name:  "Wants= and After= on one service is one edge",
			unit:  "[Unit]\nWants=db.service\nAfter=db.service cache.service\n",
			waits: []string{"db", "cache"},
		},
		{
			name:    "BindsTo= is depends-on, and After= adds no second edge",
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
			waits: []string{"db"},
		},
		{
			name:         "PartOf= has no equivalent and says so",
			unit:         "[Unit]\nPartOf=db.service\n",
			noteContains: "PartOf=db.service has no slinit equivalent",
		},
		{
			name:             "StopWhenUnneeded=yes is stop-when-unneeded",
			unit:             "[Unit]\nStopWhenUnneeded=yes\n",
			stopWhenUnneeded: true,
			notWarnFor:       "StopWhenUnneeded",
		},
		{
			name:       "StopWhenUnneeded=no",
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
			eq("after", desc.After, nil)
			eq("before", desc.Before, nil)
			var asserts []string
			for _, p := range desc.Predicates {
				if strings.Contains(p.String(), "service-started") {
					asserts = append(asserts, p.Param)
				}
			}
			eq("assert-service-started", asserts, tc.asserts)
			if desc.StopWhenUnneeded != tc.stopWhenUnneeded {
				t.Errorf("stop-when-unneeded = %v, want %v", desc.StopWhenUnneeded, tc.stopWhenUnneeded)
			}
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
