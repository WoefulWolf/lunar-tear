package patch

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// For the current loop instance, availability-window content is rewritten into
// compressed real-date windows so the schedule rotates locally over the loop.
// Everything else with a schedule is extended to 2030, maintenance is emptied,
// and the gimmick and omikuji tables are left alone. Deterministic for a given
// (anchor, loop, now-within-instance).

const (
	msDay  = 86400000
	msHour = 3600000

	realLo       int64 = 1546300800000 // 2019-01-01Z
	realHi       int64 = 1893456000000 // 2030-01-01Z
	extendTo     int64 = 1924991999000 // 2030-12-31T23:59:59Z
	disableStart int64 = 4102444800000 // 2100-01-01Z — "never starts"

	shopPremium  int64 = 1 // m_shop.ShopGroupType: real-money
	shopExchange int64 = 4 // banner exchange
)

var (
	emptyTables         = map[string]bool{"m_maintenance": true}
	leaveOriginalTables = map[string]bool{"m_gimmick_sequence_schedule": true, "m_omikuji": true}
)

// DefaultKeepMissionCategories is every mission category except events (3, 4,
// 10). Terms used by these stay permanent so all daily/recurring instances are
// live at once, instead of collapsing to the one in the current window.
func DefaultKeepMissionCategories() map[int64]bool {
	return map[int64]bool{1: true, 2: true, 5: true, 6: true, 7: true, 9: true}
}

// Schemas maps a table name to its positional column layout.
type Schemas map[string]TableSchema

type TableSchema struct {
	Columns [][]any `json:"columns"`
}

func ParseSchemas(raw []byte) (Schemas, error) {
	var s Schemas
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse schemas: %w", err)
	}
	return s, nil
}

// cols returns (position, name) for each column of a table.
func (s Schemas) cols(table string) [][2]any {
	e, ok := s[table]
	if !ok {
		return nil
	}
	out := make([][2]any, 0, len(e.Columns))
	for _, c := range e.Columns {
		if len(c) < 3 {
			continue
		}
		pos, ok := c[0].(float64)
		if !ok {
			continue
		}
		name, _ := c[2].(string)
		out = append(out, [2]any{int(pos), name})
	}
	return out
}

// index returns name -> column position.
func (s Schemas) index(table string) map[string]int {
	m := map[string]int{}
	for _, c := range s.cols(table) {
		if n, _ := c[1].(string); n != "" {
			m[n] = c[0].(int)
		}
	}
	return m
}

// datePairs finds Start/End column pairs sharing a prefix, plus End columns with
// no matching Start.
func (s Schemas) datePairs(table string) (pairs [][2]int, loneEnds []int) {
	const sSuf, eSuf = "StartDatetime", "EndDatetime"
	starts, ends := map[string]int{}, map[string]int{}
	for _, c := range s.cols(table) {
		name, _ := c[1].(string)
		pos := c[0].(int)
		if len(name) > len(sSuf) && name[len(name)-len(sSuf):] == sSuf {
			starts[name[:len(name)-len(sSuf)]] = pos
		} else if name == sSuf {
			starts[""] = pos
		}
		if len(name) > len(eSuf) && name[len(name)-len(eSuf):] == eSuf {
			ends[name[:len(name)-len(eSuf)]] = pos
		} else if name == eSuf {
			ends[""] = pos
		}
	}
	keys := make([]string, 0, len(starts))
	for k := range starts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if e, ok := ends[k]; ok {
			pairs = append(pairs, [2]int{starts[k], e})
		}
	}
	ekeys := make([]string, 0, len(ends))
	for k := range ends {
		ekeys = append(ekeys, k)
	}
	sort.Strings(ekeys)
	for _, k := range ekeys {
		if _, ok := starts[k]; !ok {
			loneEnds = append(loneEnds, ends[k])
		}
	}
	return pairs, loneEnds
}

type Options struct {
	LoopDays        float64
	MinHours        float64
	SignpostCap     int
	ShopRefreshGems []int
	RoundMinutes    int
	AnchorMillis    int64
	NowMillis       int64
	Availability    map[string]bool
	TimelineStart   int64 // 0 = auto-derive
	TimelineEnd     int64 // 0 = auto-derive
	Schemas         Schemas
	Names           Names
	WantCalendar    bool
	// KeepMissionCategories are the mission categories whose terms stay
	// permanent; nil means DefaultKeepMissionCategories.
	KeepMissionCategories map[int64]bool
	// CustomContent lists server-authored rows appended per table.
	CustomContent CustomContent
}

// Names maps asset/text ids to display names for the in-game calendar.
type Names struct {
	Banners  map[string]string `json:"banners"`
	Events   map[string]string `json:"events"`
	Missions map[string]string `json:"missions"`
}

type CalendarEntry struct {
	Name  string `json:"name"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
}

type Calendar struct {
	GeneratedAt int64           `json:"generatedAt"`
	LoopStart   int64           `json:"loopStart"`
	LoopDays    float64         `json:"loopDays"`
	Banners     []CalendarEntry `json:"banners"`
	Events      []CalendarEntry `json:"events"`
	Missions    []CalendarEntry `json:"missions"`
}

type Stats struct {
	Compress, Extend             int
	ShopDisabled, ShopExchange   int
	ShopPerm, SignpostCapEffect  int
	LoopIndex, LoopStart, T0, T1 int64
}

type rotator struct {
	opt       Options
	loop      int64
	minMillis int64
	roundMs   int64
	t0, span  int64
	loopStart int64
	stats     Stats
}

// Rotate rewrites the archive in place and returns the calendar (when requested)
// plus statistics.
func Rotate(a *Archive, opt Options) (*Calendar, Stats, error) {
	if opt.LoopDays <= 0 {
		return nil, Stats{}, fmt.Errorf("loop-days must be > 0")
	}
	if opt.KeepMissionCategories == nil {
		opt.KeepMissionCategories = DefaultKeepMissionCategories()
	}
	r := &rotator{
		opt:       opt,
		loop:      int64(opt.LoopDays * msDay),
		minMillis: int64(opt.MinHours * msHour),
		roundMs:   int64(max(0, opt.RoundMinutes)) * 60 * 1000,
	}
	if err := r.deriveTimeline(a); err != nil {
		return nil, Stats{}, err
	}

	loopIndex := floorDiv(opt.NowMillis-opt.AnchorMillis, r.loop)
	r.loopStart = opt.AnchorMillis + loopIndex*r.loop
	r.stats.LoopIndex = loopIndex
	r.stats.LoopStart = r.loopStart

	cal := &Calendar{
		GeneratedAt: opt.NowMillis,
		LoopStart:   r.loopStart,
		LoopDays:    opt.LoopDays,
		Banners:     []CalendarEntry{},
		Events:      []CalendarEntry{},
		Missions:    []CalendarEntry{},
	}

	keepTerms := r.keepTerms(a)
	eventTermWindows := map[int64][2]int64{}

	// The calendar works from the untouched rows — it does its own filtering and
	// window baking — so take a copy before the rewrite loop overwrites them.
	var origRows map[string][]any
	if opt.WantCalendar {
		origRows = map[string][]any{}
		for _, t := range []string{"m_event_quest_chapter", "m_mission_group", "m_mission"} {
			origRows[t] = decodeTable(a, t)
		}
	}

	for _, name := range a.Order {
		data, err := r.rewriteTable(a, name, keepTerms, eventTermWindows, cal)
		if err != nil {
			return nil, Stats{}, fmt.Errorf("table %q: %w", name, err)
		}
		a.Tables[name] = data
	}

	if opt.WantCalendar {
		r.buildCalendar(origRows, eventTermWindows, cal)
	}
	return cal, r.stats, nil
}

func (r *rotator) deriveTimeline(a *Archive) error {
	t0, t1 := int64(math.MaxInt64), int64(0)
	if r.opt.TimelineStart == 0 || r.opt.TimelineEnd == 0 {
		for table := range r.opt.Availability {
			raw, ok := a.Tables[table]
			if !ok {
				continue
			}
			rows, err := DecodeRows(raw)
			if err != nil {
				continue
			}
			pairs, _ := r.opt.Schemas.datePairs(table)
			for _, row := range rows {
				cells, ok := row.([]any)
				if !ok {
					continue
				}
				for _, p := range pairs {
					s, e, ok := window(cells, p[0], p[1])
					if !ok {
						continue
					}
					if realLo < s && s < realHi && s < e && e < realHi {
						t0 = min(t0, s)
						t1 = max(t1, e)
					}
				}
			}
		}
	}
	if r.opt.TimelineStart != 0 {
		t0 = r.opt.TimelineStart
	}
	if r.opt.TimelineEnd != 0 {
		t1 = r.opt.TimelineEnd
	}
	r.t0, r.span = t0, t1-t0
	r.stats.T0, r.stats.T1 = t0, t1
	if r.span <= 0 {
		return fmt.Errorf("no timeline: set timeline start/end or ensure availability tables have real windows")
	}
	return nil
}

// bakeOffsets maps a real window onto loop-relative (start offset, duration).
func (r *rotator) bakeOffsets(s, e int64) (int64, int64) {
	cs := wrapMod(int64(float64(s-r.t0)/float64(r.span)*float64(r.loop)), r.loop)
	cdur := int64(float64(e-s) / float64(r.span) * float64(r.loop))
	floor := min(r.minMillis, e-s)
	if cdur < floor {
		cdur = floor
	}
	return cs, cdur
}

// absolute converts loop offsets to absolute timestamps, snapped to the minute
// grid. Snapping is applied to the absolute values so it holds for any anchor;
// a window can never collapse to zero length.
func (r *rotator) absolute(cs, cdur int64) (int64, int64) {
	s, e := r.loopStart+cs, r.loopStart+cs+cdur
	if r.roundMs > 0 {
		snap := func(t int64) int64 { return floorDiv(t+r.roundMs/2, r.roundMs) * r.roundMs }
		s, e = snap(s), snap(e)
		if e <= s {
			e = s + r.roundMs
		}
	}
	return s, e
}

func (r *rotator) bake(s, e int64) (int64, int64) { return r.absolute(r.bakeOffsets(s, e)) }

// keepTerms collects mission terms belonging to non-event categories; those stay
// permanent so every daily/recurring instance remains available.
func (r *rotator) keepTerms(a *Archive) map[int64]bool {
	keep := map[int64]bool{}
	if !r.opt.Availability["m_mission_term"] {
		return keep
	}
	gi := r.opt.Schemas.index("m_mission_group")
	mi := r.opt.Schemas.index("m_mission")
	gg, okGG := gi["MissionGroupId"]
	gc, okGC := gi["MissionCategoryType"]
	mg, okMG := mi["MissionGroupId"]
	mt, okMT := mi["MissionTermId"]
	if !okGG || !okGC || !okMG || !okMT {
		return keep
	}

	catByGroup := map[int64]int64{}
	for _, row := range decodeTable(a, "m_mission_group") {
		cells, ok := row.([]any)
		if !ok || gg >= len(cells) || gc >= len(cells) {
			continue
		}
		id, err1 := AsInt(cells[gg])
		cat, err2 := AsInt(cells[gc])
		if err1 == nil && err2 == nil {
			catByGroup[id] = cat
		}
	}
	for _, row := range decodeTable(a, "m_mission") {
		cells, ok := row.([]any)
		if !ok || mg >= len(cells) || mt >= len(cells) {
			continue
		}
		grp, err1 := AsInt(cells[mg])
		term, err2 := AsInt(cells[mt])
		if err1 == nil && err2 == nil && r.opt.KeepMissionCategories[catByGroup[grp]] {
			keep[term] = true
		}
	}
	return keep
}

func (r *rotator) rewriteTable(a *Archive, name string, keepTerms map[int64]bool,
	eventTermWindows map[int64][2]int64, cal *Calendar) ([]byte, error) {

	orig := a.Tables[name]
	pairs, loneEnds := r.opt.Schemas.datePairs(name)
	hasDates := len(pairs) > 0 || len(loneEnds) > 0

	switch {
	case emptyTables[name]:
		return []byte{0x90}, nil // empty msgpack array

	case name == "m_gacha_medal", name == "m_consumable_item":
		// Server-authored rows (see custom_content.json); the shipped data has no
		// Original Summon, and the client needs its medal + item config.
		return appendRows(orig, r.opt.CustomContent.rowsFor(name))

	case name == "m_shop_replaceable_gem" && len(r.opt.ShopRefreshGems) > 0:
		return r.rewriteShopReplaceableGem(orig)

	case leaveOriginalTables[name] || !hasDates:
		return orig, nil

	case name == "m_shop":
		return r.rewriteShop(orig, name)

	case name == "m_mission_term":
		return r.rewriteMissionTerm(orig, name, keepTerms, eventTermWindows)

	case name == "m_mom_banner":
		return r.rewriteMomBanner(orig, name, cal)

	case name == "m_consumable_item_term":
		return r.rewriteConsumableItemTerm(orig, name)

	default:
		return r.rewriteGeneric(orig, name, pairs, loneEnds)
	}
}

func (r *rotator) rewriteShopReplaceableGem(orig []byte) ([]byte, error) {
	rows, err := DecodeRows(orig)
	if err != nil || rows == nil {
		return orig, nil
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, _ := firstInt(rows[i])
		b, _ := firstInt(rows[j])
		return a < b
	})
	gems := r.opt.ShopRefreshGems
	for i, row := range rows {
		cells, ok := row.([]any)
		if !ok || len(cells) < 2 {
			continue
		}
		cells[1] = int64(gems[min(i, len(gems)-1)])
	}
	return EncodeRows(rows)
}

func (r *rotator) rewriteShop(orig []byte, name string) ([]byte, error) {
	rows, err := DecodeRows(orig)
	if err != nil || rows == nil {
		return orig, nil
	}
	idx := r.opt.Schemas.index(name)
	gt, ok1 := idx["ShopGroupType"]
	sc, ok2 := idx["StartDatetime"]
	ec, ok3 := idx["EndDatetime"]
	if !ok1 || !ok2 || !ok3 {
		return orig, nil
	}
	for _, row := range rows {
		cells, ok := row.([]any)
		if !ok || maxInt(gt, sc, ec) >= len(cells) {
			continue
		}
		typ, err := AsInt(cells[gt])
		if err != nil {
			continue
		}
		switch {
		case typ == shopPremium:
			cells[sc] = disableStart
			r.stats.ShopDisabled++
		case typ == shopExchange:
			if s, e, ok := window(cells, sc, ec); ok && realLo < s && s < realHi && s < e {
				cells[sc], cells[ec] = r.bake(s, e)
				r.stats.ShopExchange++
			}
		default:
			if e, err := AsInt(cells[ec]); err == nil && realLo < e && e < realHi {
				cells[ec] = extendTo
				r.stats.ShopPerm++
			}
		}
	}
	return EncodeRows(rows)
}

func (r *rotator) rewriteMissionTerm(orig []byte, name string, keepTerms map[int64]bool,
	eventTermWindows map[int64][2]int64) ([]byte, error) {

	rows, err := DecodeRows(orig)
	if err != nil || rows == nil {
		return orig, nil
	}
	idx := r.opt.Schemas.index(name)
	ti, ok1 := idx["MissionTermId"]
	sc, ok2 := idx["StartDatetime"]
	ec, ok3 := idx["EndDatetime"]
	if !ok1 || !ok2 || !ok3 {
		return orig, nil
	}
	for _, row := range rows {
		cells, ok := row.([]any)
		if !ok || maxInt(ti, sc, ec) >= len(cells) {
			continue
		}
		termId, err := AsInt(cells[ti])
		if err != nil {
			continue
		}
		// Not in the compress list, or a category we keep permanent: extend the
		// end instead of pinning the term into a loop slot.
		if !r.opt.Availability[name] || keepTerms[termId] {
			if e, err := AsInt(cells[ec]); err == nil && realLo < e && e < realHi {
				cells[ec] = extendTo
				r.stats.Extend++
			}
			continue
		}
		if s, e, ok := window(cells, sc, ec); ok && realLo < s && s < e && e < realHi {
			ns, ne := r.bake(s, e)
			cells[sc], cells[ec] = ns, ne
			r.stats.Compress++
			eventTermWindows[termId] = [2]int64{ns, ne}
		}
	}
	return EncodeRows(rows)
}

func (r *rotator) rewriteMomBanner(orig []byte, name string, cal *Calendar) ([]byte, error) {
	rows, err := DecodeRows(orig)
	if err != nil || rows == nil {
		return orig, nil
	}
	idx := r.opt.Schemas.index(name)
	sc, ok1 := idx["StartDatetime"]
	ec, ok2 := idx["EndDatetime"]
	if !ok1 || !ok2 {
		return orig, nil
	}

	// Banners are only compressed when the table is in the availability list;
	// otherwise their windows are extended like any other scheduled table.
	if !r.opt.Availability[name] {
		for _, row := range rows {
			cells, ok := row.([]any)
			if !ok || ec >= len(cells) {
				continue
			}
			if e, err := AsInt(cells[ec]); err == nil && realLo < e && e < realHi {
				cells[ec] = extendTo
				r.stats.Extend++
			}
		}
		rows = append(rows, r.opt.CustomContent.rowsFor(name)...)
		return EncodeRows(rows)
	}

	type rot struct {
		cells       []any
		cs, cdur    int64
		calEligible bool
	}
	var rotating []rot
	for _, row := range rows {
		cells, ok := row.([]any)
		if !ok || maxInt(sc, ec) >= len(cells) {
			continue
		}
		if s, e, ok := window(cells, sc, ec); ok && realLo < s && s < e && e < realHi {
			cs, cdur := r.bakeOffsets(s, e)
			rotating = append(rotating, rot{cells: cells, cs: cs, cdur: cdur})
		}
	}

	if r.opt.SignpostCap > 0 && len(rotating) > 0 {
		// Permanent always-on banners occupy signpost dots too, so shrink the
		// rotating cap by their count (+1 for the custom summon appended below).
		perm := 1
		for _, row := range rows {
			cells, ok := row.([]any)
			if !ok || maxInt(sc, ec) >= len(cells) {
				continue
			}
			e, err1 := AsInt(cells[ec])
			s, err2 := AsInt(cells[sc])
			if err1 == nil && err2 == nil && e >= realHi && s < realHi {
				perm++
			}
		}
		eff := max(1, r.opt.SignpostCap-perm)
		windows := make([][3]int64, len(rotating))
		for i, w := range rotating {
			windows[i] = [3]int64{w.cs, w.cdur, int64(i)}
		}
		capped := r.fifoSignpostCap(windows, eff)
		for i := range rotating {
			rotating[i].cdur = capped[int64(i)]
		}
		r.stats.SignpostCapEffect = eff
	}
	for _, w := range rotating {
		w.cells[sc], w.cells[ec] = r.absolute(w.cs, w.cdur)
	}
	r.stats.Compress += len(rotating)

	// Named gacha banners for the in-game calendar (post-cap windows).
	if r.opt.WantCalendar {
		dt, okDT := idx["DestinationDomainType"]
		an, okAN := idx["BannerAssetName"]
		if okDT && okAN {
			for _, w := range rotating {
				if maxInt(dt, an) >= len(w.cells) {
					continue
				}
				if d, err := AsInt(w.cells[dt]); err != nil || d != 1 { // 1 = gacha/summon
					continue
				}
				asset, _ := w.cells[an].(string)
				if title := r.opt.Names.Banners[asset]; title != "" {
					s, _ := AsInt(w.cells[sc])
					e, _ := AsInt(w.cells[ec])
					cal.Banners = append(cal.Banners, CalendarEntry{Name: title, Start: s, End: e})
				}
			}
		}
	}

	rows = append(rows, r.opt.CustomContent.rowsFor(name)...)
	return EncodeRows(rows)
}

// fifoSignpostCap keeps at most cap banners active at once by FIFO-truncating:
// when a new banner starts and the cap is full, the oldest-started active banner
// is cut to the newcomer's start. Simulated over three loop copies so the
// cross-boundary state is steady. windows are (cs, cdur, key).
func (r *rotator) fifoSignpostCap(windows [][3]int64, cap int) map[int64]int64 {
	type inst struct {
		start, end, key, copyIdx int64
	}
	var all []*inst
	for _, k := range []int64{-1, 0, 1} {
		for _, w := range windows {
			st := w[0] + k*r.loop
			all = append(all, &inst{start: st, end: st + w[1], key: w[2], copyIdx: k})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].start < all[j].start })

	type finalKey struct{ key, copyIdx int64 }
	final := map[finalKey]int64{}
	var active []*inst
	for _, cur := range all {
		t := cur.start
		keep := active[:0]
		for _, a := range active {
			if a.end <= t {
				final[finalKey{a.key, a.copyIdx}] = a.end
			} else {
				keep = append(keep, a)
			}
		}
		active = keep
		if len(active) >= cap {
			victim, vi := active[0], 0
			for i, a := range active {
				if a.start < victim.start {
					victim, vi = a, i
				}
			}
			victim.end = t
			final[finalKey{victim.key, victim.copyIdx}] = t
			active = append(active[:vi], active[vi+1:]...)
		}
		active = append(active, cur)
	}
	for _, a := range active {
		final[finalKey{a.key, a.copyIdx}] = a.end
	}

	out := make(map[int64]int64, len(windows))
	for _, w := range windows {
		out[w[2]] = final[finalKey{w[2], 0}] - w[0]
	}
	return out
}

func (r *rotator) rewriteConsumableItemTerm(orig []byte, name string) ([]byte, error) {
	rows, err := DecodeRows(orig)
	if err != nil || rows == nil {
		return orig, nil
	}
	if ec, ok := r.opt.Schemas.index(name)["EndDatetime"]; ok {
		for _, row := range rows {
			cells, ok := row.([]any)
			if !ok || ec >= len(cells) {
				continue
			}
			if e, err := AsInt(cells[ec]); err == nil && realLo < e && e < realHi {
				cells[ec] = extendTo
				r.stats.Extend++
			}
		}
	}
	rows = append(rows, r.opt.CustomContent.rowsFor(name)...)
	return EncodeRows(rows)
}

func (r *rotator) rewriteGeneric(orig []byte, name string, pairs [][2]int, loneEnds []int) ([]byte, error) {
	rows, err := DecodeRows(orig)
	if err != nil || rows == nil {
		return orig, nil
	}
	compress := r.opt.Availability[name]
	for _, row := range rows {
		cells, ok := row.([]any)
		if !ok {
			continue
		}
		if compress {
			for _, p := range pairs {
				if s, e, ok := window(cells, p[0], p[1]); ok && realLo < s && s < e && e < realHi {
					// Bounded real window -> compress. A permanent-end window
					// ("unlocks 2024-01, never expires") is left alone so it stays
					// available instead of being pinned into a short loop slot.
					cells[p[0]], cells[p[1]] = r.bake(s, e)
					r.stats.Compress++
				}
			}
			continue
		}
		ends := make([]int, 0, len(pairs)+len(loneEnds))
		for _, p := range pairs {
			ends = append(ends, p[1])
		}
		ends = append(ends, loneEnds...)
		for _, ec := range ends {
			if ec >= len(cells) {
				continue
			}
			if e, err := AsInt(cells[ec]); err == nil && realLo < e && e < realHi {
				cells[ec] = extendTo
				r.stats.Extend++
			}
		}
	}
	return EncodeRows(rows)
}

func (r *rotator) buildCalendar(origRows map[string][]any, eventTermWindows map[int64][2]int64, cal *Calendar) {
	// Event quest chapters. Windows come from the pristine rows and are baked
	// here; rows without a bounded real window (permanent content such as
	// Guerrilla Quests) are not schedule entries and are skipped.
	if r.opt.Availability["m_event_quest_chapter"] {
		idx := r.opt.Schemas.index("m_event_quest_chapter")
		esc, ok1 := idx["StartDatetime"]
		eec, ok2 := idx["EndDatetime"]
		if ok1 && ok2 {
			for _, row := range origRows["m_event_quest_chapter"] {
				cells, ok := row.([]any)
				if !ok || maxInt(esc, eec) >= len(cells) {
					continue
				}
				s, e, ok := window(cells, esc, eec)
				if !ok || !(realLo < s && s < e && e < realHi) {
					continue
				}
				id, err := firstInt(row)
				if err != nil {
					continue
				}
				if title := r.opt.Names.Events[fmt.Sprint(id)]; title != "" {
					es, ee := r.bake(s, e)
					cal.Events = append(cal.Events, CalendarEntry{Name: title, Start: es, End: ee})
				}
			}
		}
	}

	// Timed mission sets: an event-only term supplies the window; the display name
	// comes from the group its missions belong to.
	if len(eventTermWindows) > 0 && len(r.opt.Names.Missions) > 0 {
		gi := r.opt.Schemas.index("m_mission_group")
		mi := r.opt.Schemas.index("m_mission")
		gId, ok1 := gi["MissionGroupId"]
		gLab, ok2 := gi["LabelMissionTextId"]
		mGrp, ok3 := mi["MissionGroupId"]
		mTerm, ok4 := mi["MissionTermId"]
		if ok1 && ok2 && ok3 && ok4 {
			labelByGroup := map[int64]int64{}
			for _, row := range origRows["m_mission_group"] {
				cells, ok := row.([]any)
				if !ok || maxInt(gId, gLab) >= len(cells) {
					continue
				}
				id, err1 := AsInt(cells[gId])
				lab, err2 := AsInt(cells[gLab])
				if err1 == nil && err2 == nil {
					labelByGroup[id] = lab
				}
			}
			seen := map[string]bool{}
			for _, row := range origRows["m_mission"] {
				cells, ok := row.([]any)
				if !ok || maxInt(mGrp, mTerm) >= len(cells) {
					continue
				}
				grp, err1 := AsInt(cells[mGrp])
				term, err2 := AsInt(cells[mTerm])
				if err1 != nil || err2 != nil {
					continue
				}
				win, ok := eventTermWindows[term]
				if !ok {
					continue
				}
				title := r.opt.Names.Missions[fmt.Sprint(labelByGroup[grp])]
				if title == "" {
					continue
				}
				key := fmt.Sprintf("%s|%d|%d", title, win[0], win[1])
				if seen[key] { // many missions share one set + window
					continue
				}
				seen[key] = true
				cal.Missions = append(cal.Missions, CalendarEntry{Name: title, Start: win[0], End: win[1]})
			}
		}
	}

	byStart := func(e []CalendarEntry) {
		sort.SliceStable(e, func(i, j int) bool { return e[i].Start < e[j].Start })
	}
	byStart(cal.Banners)
	byStart(cal.Events)
	byStart(cal.Missions)
}

func decodeTable(a *Archive, name string) []any {
	raw, ok := a.Tables[name]
	if !ok {
		return nil
	}
	rows, err := DecodeRows(raw)
	if err != nil {
		return nil
	}
	return rows
}

// window reads a (start, end) integer pair from a row, reporting false when
// either cell is missing or not an integer.
func window(cells []any, sc, ec int) (int64, int64, bool) {
	if sc >= len(cells) || ec >= len(cells) {
		return 0, 0, false
	}
	s, err1 := AsInt(cells[sc])
	e, err2 := AsInt(cells[ec])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return s, e, true
}

func firstInt(row any) (int64, error) {
	cells, ok := row.([]any)
	if !ok || len(cells) == 0 {
		return 0, fmt.Errorf("empty row")
	}
	return AsInt(cells[0])
}

func appendRows(orig []byte, extra []any) ([]byte, error) {
	if len(extra) == 0 {
		return orig, nil
	}
	rows, err := DecodeRows(orig)
	if err != nil || rows == nil {
		return orig, nil
	}
	return EncodeRows(append(rows, extra...))
}

func maxInt(v ...int) int {
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

// floorDiv rounds toward negative infinity, unlike Go's truncating division.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// wrapMod returns a non-negative remainder, so a window offset always lands
// inside the loop even when the timeline start is ahead of the window.
func wrapMod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}
