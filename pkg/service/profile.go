package service

import (
	"fmt"
	"sort"
)

// ProfileActivationResult reports what ActivateProfile did.
// Callers (control connection, boot flow) use it to shape their
// response to the operator without having to re-scan the set.
type ProfileActivationResult struct {
	// Previous is the profile that was active before this call.
	// Empty string means "no profile filter was active".
	Previous string
	// Active is the profile that is now active. Empty means the
	// operator explicitly cleared filtering ("deactivate").
	Active string
	// Stopped lists services that transitioned toward stopped
	// because they were tagged for the outgoing profile but not
	// the incoming one.
	Stopped []string
	// Started lists services that were brought up because they
	// were tagged for the incoming profile but not the outgoing.
	Started []string
	// Kept lists services that were tagged for both profiles
	// (or are global) and were left alone. Useful for reporting.
	Kept []string
}

// SetActiveProfile records the initial active profile without
// running any deltas — used at boot before services are loaded, so
// the initial LoadService pass can filter correctly.
func (ss *ServiceSet) SetActiveProfile(name string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.activeProfile = name
}

// ActiveProfile returns the currently active profile name.
func (ss *ServiceSet) ActiveProfile() string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.activeProfile
}

// ListProfiles returns the sorted distinct set of profile tags
// declared by any currently loaded service. Global (no-profile)
// services are not included.
func (ss *ServiceSet) ListProfiles() []string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	seen := make(map[string]struct{})
	for _, svc := range ss.records {
		for _, p := range svc.Record().Profiles() {
			seen[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ActivateProfile swaps the active profile. Services tagged for the
// outgoing profile but not the incoming one are stopped; services
// tagged for the incoming profile but not the outgoing one are
// started; services in both (or global) are left alone.
//
// Global services (no profile tags) are always considered "in" every
// profile, so they never bounce during a swap — this is the
// property that makes cross-profile activation safe for shared
// infrastructure like the network stack or the shared logger.
//
// The dependency graph does the heavy lifting for actually driving
// the state changes: BringUp on a service pulls in its dependencies,
// BringDown propagates to dependents. ActivateProfile just picks the
// right entry points.
//
// Passing "" as newProfile deactivates filtering — nothing is
// stopped, nothing is started, but the recorded activeProfile is
// cleared so subsequent LoadService calls no longer filter. Useful
// for admins who want to boot in a profile and later drop the
// filter without a reboot.
func (ss *ServiceSet) ActivateProfile(newProfile string) (*ProfileActivationResult, error) {
	ss.mu.Lock()
	oldProfile := ss.activeProfile
	if newProfile == oldProfile {
		ss.mu.Unlock()
		return &ProfileActivationResult{
			Previous: oldProfile,
			Active:   newProfile,
		}, nil
	}

	// Validate: if newProfile is non-empty, at least one loaded
	// service must claim it. Silently activating a typo'd profile
	// name would stop every profile-tagged service — that's the
	// kind of "shouldn't happen" the parser can't catch, so we
	// enforce it here.
	if newProfile != "" {
		found := false
		for _, svc := range ss.records {
			for _, p := range svc.Record().Profiles() {
				if p == newProfile {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			ss.mu.Unlock()
			return nil, fmt.Errorf("no loaded service declares profile %q", newProfile)
		}
	}

	// Categorize every currently-loaded service against the swap.
	var toStop, toStart, keep []Service
	for _, svc := range ss.records {
		rec := svc.Record()
		if len(rec.Profiles()) == 0 {
			// Global — always kept.
			keep = append(keep, svc)
			continue
		}
		inOld := oldProfile != "" && rec.InProfile(oldProfile)
		inNew := newProfile != "" && rec.InProfile(newProfile)
		switch {
		case inOld && !inNew:
			toStop = append(toStop, svc)
		case !inOld && inNew:
			toStart = append(toStart, svc)
		default:
			keep = append(keep, svc)
		}
	}
	ss.activeProfile = newProfile
	ss.mu.Unlock()

	result := &ProfileActivationResult{
		Previous: oldProfile,
		Active:   newProfile,
	}

	// Stop first so freed dependencies release cleanly before the
	// incoming set tries to acquire them. Iterate services outside
	// the mutex — BringDown/BringUp reach back into ServiceSet and
	// grab ss.queueMu themselves, so we must not be holding ss.mu.
	for _, svc := range toStop {
		result.Stopped = append(result.Stopped, svc.Name())
		svc.BringDown()
	}
	for _, svc := range toStart {
		result.Started = append(result.Started, svc.Name())
		svc.BringUp()
	}
	for _, svc := range keep {
		if len(svc.Record().Profiles()) == 0 {
			continue
		}
		result.Kept = append(result.Kept, svc.Name())
	}

	sort.Strings(result.Stopped)
	sort.Strings(result.Started)
	sort.Strings(result.Kept)
	return result, nil
}

// ProfileAllows reports whether a service description with the given
// profile tags is eligible under the currently active profile. Used
// by the loader to filter which services become auto-started at boot.
func (ss *ServiceSet) ProfileAllows(profiles []string) bool {
	ss.mu.Lock()
	active := ss.activeProfile
	ss.mu.Unlock()
	if active == "" {
		return true
	}
	if len(profiles) == 0 {
		return true // global
	}
	for _, p := range profiles {
		if p == active {
			return true
		}
	}
	return false
}
