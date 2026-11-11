package cvm

import (
	"strconv"
	"strings"
)

// The memory balloon's arithmetic (the runner, on the Mac, uses it).

// portMem is the vsock port where the guest answers with its /proc/meminfo.
const portMem = 2379

// memInfo is what matters of the guest's /proc/meminfo, in bytes.
type memInfo struct {
	Total, Available, Cached uint64
}

// Used is what the guest needs: its total minus what it could free now.
func (m memInfo) Used() uint64 {
	if m.Available > m.Total {
		return 0
	}
	return m.Total - m.Available
}

func parseMemInfo(s string) (memInfo, error) {
	var m memInfo
	seen := 0
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		n *= 1024
		switch k {
		case "MemTotal":
			m.Total = n
			seen++
		case "MemAvailable":
			m.Available = n
			seen++
		case "Cached":
			m.Cached = n
		}
	}
	if seen < 2 || m.Total == 0 {
		return m, errMemInfo
	}
	return m, nil
}

type memErr string

func (e memErr) Error() string { return string(e) }

const errMemInfo = memErr("meminfo incompleto")

const (
	mib = 1 << 20
	gib = 1 << 30
)

// balloonTarget is the memory the machine should have now. The guest's
// MemTotal usually excludes the inflated balloon, so used is what the
// guest really holds; the room on top is a quarter of it, at least
// 768 MiB, and the whole never goes under 1 GiB or over max.
func balloonTarget(m memInfo, cur, max uint64) uint64 {
	used := m.Used()
	// Some guests keep MemTotal at the full size and count the balloon's
	// pages as used: then they are taken out, or the balloon would chase
	// its own tail.
	if inflated := max - cur; cur < max && m.Total > cur+inflated/2 {
		if used > inflated {
			used -= inflated
		} else {
			used = 0
		}
	}
	room := used / 4
	if room < 768*mib {
		room = 768 * mib
	}
	want := used + room
	// Little room left in the guest: give it a good step at once.
	if m.Available < 384*mib {
		want = cur + 1*gib
	}
	if want < 1*gib {
		want = 1 * gib
	}
	if want > max {
		want = max
	}
	// Round to 128 MiB, so small changes do not move the balloon.
	want = (want + 128*mib - 1) / (128 * mib) * (128 * mib)
	if want > max {
		want = max
	}
	return want
}

// MemoryState is what the page shows of the balloon.
type MemoryState struct {
	Balloon    bool   `json:"balloon"`
	Max        uint64 `json:"max"`
	Target     uint64 `json:"target"`
	Returned   uint64 `json:"returned"`
	GuestUsed  uint64 `json:"guestUsed"`
	GuestCache uint64 `json:"guestCache"`
	At         int64  `json:"at"`
}

func (s Spec) balloonOn() bool { return !s.NoBalloon }
