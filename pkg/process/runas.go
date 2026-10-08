package process

import (
	"fmt"
	"os/user"
	"strconv"
	"strings"
)

// ResolveRunAs turns a `run-as` value — user[:group], each a name or a
// numeric id — into a UID and GID. Without a group, the user's primary
// GID is used. Any part that does not resolve is an error: the caller
// must not fall back to running as itself, which for slinit is root.
func ResolveRunAs(spec string) (uid, gid uint32, err error) {
	userPart, groupPart, _ := strings.Cut(spec, ":")
	userPart = strings.TrimSpace(userPart)
	groupPart = strings.TrimSpace(groupPart)
	if userPart == "" {
		return 0, 0, fmt.Errorf("run-as %q: no user", spec)
	}

	u, err := user.Lookup(userPart)
	if err != nil {
		if u, err = user.LookupId(userPart); err != nil {
			return 0, 0, fmt.Errorf("run-as: user %q not found", userPart)
		}
	}
	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("run-as: user %q: bad uid %q", userPart, u.Uid)
	}
	gidStr := u.Gid

	if groupPart != "" {
		g, err := user.LookupGroup(groupPart)
		if err != nil {
			if g, err = user.LookupGroupId(groupPart); err != nil {
				return 0, 0, fmt.Errorf("run-as: group %q not found", groupPart)
			}
		}
		gidStr = g.Gid
	}
	gid64, err := strconv.ParseUint(gidStr, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("run-as %q: bad gid %q", spec, gidStr)
	}
	return uint32(uid64), uint32(gid64), nil
}
