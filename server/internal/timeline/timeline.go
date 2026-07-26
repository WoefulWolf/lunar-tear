// Package timeline decides where in the rotated content loop a player is.
//
// In per-player mode a player's loop starts the day they registered. The loop is
// periodic, so that's just a time offset — the master data is untouched, only the
// clock availability windows are compared against.
//
// ContentMillis is a separate type so wall-clock time can't be passed by
// mistake. Real-world things (daily reset, login bonus, gacha draw limits) stay
// on plain millis.
package timeline

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"lunar-tear/server/internal/gametime"
)

const msDay = 86400000

// ContentMillis is a unix-millis timestamp on the player's content clock.
type ContentMillis int64

type Mode int

const (
	// ModeGlobal puts every player at the same point in the loop.
	ModeGlobal Mode = iota
	// ModePerPlayer starts each player's loop on the day they registered.
	ModePerPlayer
)

func (m Mode) String() string {
	if m == ModePerPlayer {
		return "per-player"
	}
	return "global"
}

// Resolver answers which timeline a player is on. Global mode collapses every
// answer to the same one, so callers don't branch.
type Resolver struct {
	mode       Mode
	anchor     int64
	loopMillis int64
}

func NewResolver(mode Mode, anchorMillis int64, loopDays float64) (*Resolver, error) {
	if mode == ModePerPlayer && loopDays <= 0 {
		return nil, fmt.Errorf("per-player mode needs a positive loop length, got %v days", loopDays)
	}
	return &Resolver{
		mode:       mode,
		anchor:     anchorMillis,
		loopMillis: int64(loopDays * msDay),
	}, nil
}

// FromEnv reads LUNAR_TIMELINE_MODE, LUNAR_TIMELINE_ANCHOR and
// LUNAR_TIMELINE_LOOP_DAYS. Unset means global mode.
//
// The anchor and loop length must match what the archives were generated with —
// they turn a registration date into a phase, so a mismatch puts the server and
// the client on different schedules.
func FromEnv() (*Resolver, error) {
	mode := ModeGlobal
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("LUNAR_TIMELINE_MODE"))); v {
	case "", "global":
	case "per-player", "perplayer":
		mode = ModePerPlayer
	default:
		return nil, fmt.Errorf("LUNAR_TIMELINE_MODE=%q: want \"global\" or \"per-player\"", v)
	}

	var anchor int64
	if raw := strings.TrimSpace(os.Getenv("LUNAR_TIMELINE_ANCHOR")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, fmt.Errorf("LUNAR_TIMELINE_ANCHOR=%q: %w", raw, err)
		}
		anchor = t.UTC().UnixMilli()
	}

	loopDays := 0.0
	if raw := strings.TrimSpace(os.Getenv("LUNAR_TIMELINE_LOOP_DAYS")); raw != "" {
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("LUNAR_TIMELINE_LOOP_DAYS=%q: %w", raw, err)
		}
		loopDays = n
	}
	return NewResolver(mode, anchor, loopDays)
}

func (r *Resolver) Mode() Mode { return r.mode }

// ShiftFor is how far behind real time the player's content clock runs; 0 in
// global mode. Always whole days, so day boundaries stay lined up between the
// two clocks and the 00:00-PST reset still fires at one instant for everyone.
func (r *Resolver) ShiftFor(registerMillis int64) int64 {
	if r.mode != ModePerPlayer || r.loopMillis <= 0 {
		return 0
	}
	startOfDay := floorDiv(registerMillis, msDay) * msDay
	shift := wrapMod(startOfDay-r.anchor, r.loopMillis)
	return shift / msDay * msDay
}

// Cohort is the phase in whole days — everyone who registered on the same day
// shares one, and it keys the generated archive. Global mode is always 0.
func (r *Resolver) Cohort(registerMillis int64) int {
	return int(r.ShiftFor(registerMillis) / msDay)
}

// CohortCount is bounded by the loop length, not the player count — phases
// repeat every loop.
func (r *Resolver) CohortCount() int {
	if r.mode != ModePerPlayer || r.loopMillis <= 0 {
		return 1
	}
	return int(r.loopMillis / msDay)
}

// At converts a real timestamp to the player's content clock.
func (r *Resolver) At(nowMillis, registerMillis int64) ContentMillis {
	return ContentMillis(nowMillis - r.ShiftFor(registerMillis))
}

// Now is At against the current time.
func (r *Resolver) Now(registerMillis int64) ContentMillis {
	return r.At(gametime.NowMillis(), registerMillis)
}

// Process-wide resolver. Reached through NowFor rather than passed around,
// because some callers are projectors with a signature fixed by the registry.
// Starts in global mode so behaviour is unchanged until main installs one.
var defaultResolver atomic.Pointer[Resolver]

func init() {
	r, _ := NewResolver(ModeGlobal, 0, 0)
	defaultResolver.Store(r)
}

// SetDefault installs the resolver used by NowFor. Call once at startup.
func SetDefault(r *Resolver) {
	if r != nil {
		defaultResolver.Store(r)
	}
}

func Default() *Resolver { return defaultResolver.Load() }

// NowFor is the content clock for a player who registered at registerMillis.
func NowFor(registerMillis int64) ContentMillis { return Default().Now(registerMillis) }

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func wrapMod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}
