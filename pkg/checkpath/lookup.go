package checkpath

import (
	"os/user"
	"strconv"
)

// lookupUID resolves a user name or numeric uid to a numeric uid.
func lookupUID(s string) (int, error) {
	if id, err := strconv.Atoi(s); err == nil {
		return id, nil
	}
	u, err := user.Lookup(s)
	if err != nil {
		return 0, err
	}
	id, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// lookupGID resolves a group name or numeric gid to a numeric gid.
func lookupGID(s string) (int, error) {
	if id, err := strconv.Atoi(s); err == nil {
		return id, nil
	}
	g, err := user.LookupGroup(s)
	if err != nil {
		return 0, err
	}
	id, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, err
	}
	return id, nil
}
