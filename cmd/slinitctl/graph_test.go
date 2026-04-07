package main

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestGraphNodeShape(t *testing.T) {
	tests := []struct {
		stype service.ServiceType
		want  string
	}{
		{service.TypeProcess, "ellipse"},
		{service.TypeInternal, "diamond"},
		{service.TypeTriggered, "hexagon"},
		{service.TypeScripted, "box"},
		{service.TypeBGProcess, "doubleoctagon"},
	}
	for _, tt := range tests {
		got := graphNodeShape(tt.stype)
		if got != tt.want {
			t.Errorf("graphNodeShape(%v) = %q, want %q", tt.stype, got, tt.want)
		}
	}
}

func TestGraphNodeColor(t *testing.T) {
	tests := []struct {
		state    service.ServiceState
		wantFill string
	}{
		{service.StateStarted, "#c8e6c9"},
		{service.StateStopped, "#ffcdd2"},
		{service.StateStarting, "#fff9c4"},
		{service.StateStopping, "#ffe0b2"},
	}
	for _, tt := range tests {
		_, fill := graphNodeColor(tt.state)
		if fill != tt.wantFill {
			t.Errorf("graphNodeColor(%v) fill = %q, want %q", tt.state, fill, tt.wantFill)
		}
	}
}

func TestGraphEdgeStyle(t *testing.T) {
	tests := []struct {
		dt        service.DependencyType
		wantStyle string
		wantLabel string
	}{
		{service.DepRegular, "solid", ""},
		{service.DepSoft, "dashed", "soft"},
		{service.DepWaitsFor, "dashed", "waits-for"},
		{service.DepMilestone, "bold", "milestone"},
		{service.DepBefore, "dotted", "before"},
		{service.DepAfter, "dotted", "after"},
	}
	for _, tt := range tests {
		style, _, label := graphEdgeStyle(tt.dt)
		if style != tt.wantStyle {
			t.Errorf("graphEdgeStyle(%v) style = %q, want %q", tt.dt, style, tt.wantStyle)
		}
		if label != tt.wantLabel {
			t.Errorf("graphEdgeStyle(%v) label = %q, want %q", tt.dt, label, tt.wantLabel)
		}
	}
}
