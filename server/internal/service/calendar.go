package service

import (
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const calendarUpcomingDays = 7

const calendarTemplateName = "calendar.html"

//go:embed calendar.html
var calendarFS embed.FS

type calendarEntry struct {
	Name  string `json:"name"`
	Start int64  `json:"start"` // unix millis
	End   int64  `json:"end"`
}

type calendarData struct {
	GeneratedAt int64           `json:"generatedAt"`
	LoopStart   int64           `json:"loopStart"`
	Banners     []calendarEntry `json:"banners"`
	Events      []calendarEntry `json:"events"`
	Missions    []calendarEntry `json:"missions"`
}

var calendarKindLabel = map[string]string{
	"summon": "Summon", "event": "Event", "mission": "Mission",
}

type calendarRow struct {
	Kind     string // "summon" | "event" | "mission" — also the CSS class
	Label    string
	Name     string
	Start    int64
	End      int64
	UTCStart string
	UTCEnd   string
}

type calendarView struct {
	Running      []calendarRow
	Upcoming     []calendarRow
	UpcomingDays int
}

func formatCalendarUTC(millis int64) string {
	return time.UnixMilli(millis).UTC().Format("2 Jan 15:04")
}

func buildCalendarView(cal *calendarData, now time.Time) calendarView {
	type kinded struct {
		entry calendarEntry
		kind  string
	}

	all := make([]kinded, 0, len(cal.Banners)+len(cal.Events)+len(cal.Missions))
	for _, e := range cal.Banners {
		all = append(all, kinded{e, "summon"})
	}
	for _, e := range cal.Events {
		all = append(all, kinded{e, "event"})
	}
	for _, e := range cal.Missions {
		all = append(all, kinded{e, "mission"})
	}

	nowMillis := now.UnixMilli()
	horizon := now.Add(calendarUpcomingDays * 24 * time.Hour).UnixMilli()

	var running, upcoming []kinded
	for _, it := range all {
		switch {
		case it.entry.Start <= nowMillis && nowMillis < it.entry.End:
			running = append(running, it)
		case nowMillis < it.entry.Start && it.entry.Start <= horizon:
			upcoming = append(upcoming, it)
		}
	}
	sort.Slice(running, func(i, j int) bool { return running[i].entry.End < running[j].entry.End })
	sort.Slice(upcoming, func(i, j int) bool { return upcoming[i].entry.Start < upcoming[j].entry.Start })

	toRows := func(items []kinded) []calendarRow {
		rows := make([]calendarRow, 0, len(items))
		for _, it := range items {
			rows = append(rows, calendarRow{
				Kind:     it.kind,
				Label:    calendarKindLabel[it.kind],
				Name:     it.entry.Name,
				Start:    it.entry.Start,
				End:      it.entry.End,
				UTCStart: formatCalendarUTC(it.entry.Start),
				UTCEnd:   formatCalendarUTC(it.entry.End),
			})
		}
		return rows
	}

	return calendarView{
		Running:      toRows(running),
		Upcoming:     toRows(upcoming),
		UpcomingDays: calendarUpcomingDays,
	}
}

var calendarTmplCache struct {
	mu      sync.Mutex
	tmpl    *template.Template
	path    string
	modTime time.Time
}

func calendarTemplate(baseDir string) (*template.Template, error) {
	override := filepath.Join(baseDir, "assets", "release", calendarTemplateName)

	calendarTmplCache.mu.Lock()
	defer calendarTmplCache.mu.Unlock()

	if info, err := os.Stat(override); err == nil && !info.IsDir() {
		if calendarTmplCache.tmpl != nil &&
			calendarTmplCache.path == override &&
			calendarTmplCache.modTime.Equal(info.ModTime()) {
			return calendarTmplCache.tmpl, nil
		}
		if tmpl, err := template.ParseFiles(override); err != nil {
			log.Printf("[calendar] %s failed to parse, using embedded default: %v", override, err)
		} else {
			log.Printf("[calendar] using template override %s", override)
			calendarTmplCache.tmpl = tmpl
			calendarTmplCache.path = override
			calendarTmplCache.modTime = info.ModTime()
			return tmpl, nil
		}
	}

	if calendarTmplCache.tmpl != nil && calendarTmplCache.path == "" {
		return calendarTmplCache.tmpl, nil
	}
	tmpl, err := template.ParseFS(calendarFS, calendarTemplateName)
	if err != nil {
		return nil, err
	}
	calendarTmplCache.tmpl = tmpl
	calendarTmplCache.path = ""
	calendarTmplCache.modTime = time.Time{}
	return tmpl, nil
}

func (s *OctoHTTPServer) loadCalendar() (*calendarData, error) {
	raw, err := os.ReadFile(filepath.Join(s.BaseDir, "assets", "release", "calendar.json"))
	if err != nil {
		return nil, err
	}
	var cal calendarData
	if err := json.Unmarshal(raw, &cal); err != nil {
		return nil, err
	}
	return &cal, nil
}

func (s *OctoHTTPServer) renderCalendarPage(cal *calendarData, now time.Time) (string, error) {
	tmpl, err := calendarTemplate(s.BaseDir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, buildCalendarView(cal, now)); err != nil {
		return "", err
	}
	return b.String(), nil
}
