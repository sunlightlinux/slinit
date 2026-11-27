package config

import "slices"

// [Unit] dependency directives the loader classifies through the
// registry: the ones with a slinit mapping are bespoke cases in
// applyUnitKey (Requires=, Wants=, After=, Before=, BindsTo=, ...),
// because they feed the dependency lists rather than one setting.

func init() {
	gaps := map[string]sdGap{
		"Upholds":              {why: "slinit does not restart that unit whenever it stops; give it `restart = yes`"},
		"PropagatesReloadTo":   {why: "`slinitctl reload` of this service reloads only this service"},
		"PropagateReloadTo":    {why: "`slinitctl reload` of this service reloads only this service"},
		"ReloadPropagatedFrom": {why: "a reload of that unit does not reach this service"},
		"PropagateReloadFrom":  {why: "a reload of that unit does not reach this service"},
		"PropagatesStopTo": {why: "a stop of this service does not stop that unit; " +
			"make that unit `depends-on:` this one if it must stop with it"},
		"StopPropagatedFrom": {why: "a stop of that unit does not stop this service; " +
			"`depends-on:` it if this service must stop with it"},
		"JoinsNamespaceOf": {confine: true, why: "the service gets namespaces of its own, " +
			"not those of that unit (see the namespace-* settings)"},
		"RequiresMountsFor": {why: "slinit does not order services after the mounts of a path; " +
			"make sure the path is mounted first, e.g. `depends-on:` the service that mounts it"},
		"WantsMountsFor": {why: "slinit does not order services after the mounts of a path"},
		"OnSuccess": {why: "`chain-to` starts a service when this one exits 0 on its own; " +
			"systemd also starts it after a clean stop. Use chain-to if that is the case that matters"},
	}
	for k, g := range gaps {
		sdUnitNoEquivalent[k] = g
	}
	// part-of: a stop or restart of the named service reaches this one,
	// which neither starts with it nor is ordered by it. A target is
	// dropped, as for After=: slinit has no targets to stop.
	registerUnit("PartOf", sdRule{custom: func(cfg *SystemdConfig, key, val string) []SystemdWarning {
		deps, targets, self := splitUnitRefs(val, cfg.svcName)
		for _, d := range deps {
			if !slices.Contains(cfg.partOf, d) {
				cfg.partOf = append(cfg.partOf, d)
			}
		}
		return noteDroppedTargets(key, targets, self)
	}})

	// Same meaning, same values: the service stops once nothing that
	// depends on it needs it any more.
	registerUnit("StopWhenUnneeded", sdRule{key: "stop-when-unneeded"})

	for k, why := range map[string]string{
		"OnFailureJobMode": "a job mode for OnFailure=; slinit has no job queue",
		"OnSuccessJobMode": "a job mode for OnSuccess=; slinit has no job queue",
		"OnFailureIsolate": "obsolete; systemd reads it as OnFailureJobMode=isolate",
	} {
		sdUnitIgnored[k] = why
	}
}
