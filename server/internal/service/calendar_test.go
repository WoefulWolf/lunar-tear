package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// calendarNow is a fixed reference point so every case reads as an offset from it.
var calendarNow = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

func atOffset(d time.Duration) int64 {
	return calendarNow.Add(d).UnixMilli()
}

func TestBuildCalendarViewBuckets(t *testing.T) {
	cal := &calendarData{
		Banners: []calendarEntry{
			{Name: "running summon", Start: atOffset(-2 * time.Hour), End: atOffset(2 * time.Hour)},
			{Name: "soon summon", Start: atOffset(3 * time.Hour), End: atOffset(30 * time.Hour)},
		},
		Events: []calendarEntry{
			{Name: "finished event", Start: atOffset(-48 * time.Hour), End: atOffset(-1 * time.Hour)},
			{Name: "far future event", Start: atOffset(30 * 24 * time.Hour), End: atOffset(31 * 24 * time.Hour)},
		},
		Missions: []calendarEntry{
			{Name: "running mission", Start: atOffset(-time.Minute), End: atOffset(time.Minute)},
		},
	}

	view := buildCalendarView(cal, calendarNow)

	if view.UpcomingDays != calendarUpcomingDays {
		t.Errorf("UpcomingDays = %d, want %d", view.UpcomingDays, calendarUpcomingDays)
	}

	gotRunning := rowNames(view.Running)
	wantRunning := []string{"running mission", "running summon"} // sorted by soonest end
	if !equalStrings(gotRunning, wantRunning) {
		t.Errorf("Running = %v, want %v", gotRunning, wantRunning)
	}

	gotUpcoming := rowNames(view.Upcoming)
	wantUpcoming := []string{"soon summon"} // finished and far-future are excluded
	if !equalStrings(gotUpcoming, wantUpcoming) {
		t.Errorf("Upcoming = %v, want %v", gotUpcoming, wantUpcoming)
	}
}

func TestBuildCalendarViewKindsAndLabels(t *testing.T) {
	cal := &calendarData{
		Banners:  []calendarEntry{{Name: "b", Start: atOffset(time.Hour), End: atOffset(2 * time.Hour)}},
		Events:   []calendarEntry{{Name: "e", Start: atOffset(2 * time.Hour), End: atOffset(3 * time.Hour)}},
		Missions: []calendarEntry{{Name: "m", Start: atOffset(3 * time.Hour), End: atOffset(4 * time.Hour)}},
	}

	view := buildCalendarView(cal, calendarNow)

	if len(view.Upcoming) != 3 {
		t.Fatalf("Upcoming has %d rows, want 3", len(view.Upcoming))
	}
	wantKinds := []string{"summon", "event", "mission"}
	wantLabels := []string{"Summon", "Event", "Mission"}
	for i, row := range view.Upcoming {
		if row.Kind != wantKinds[i] {
			t.Errorf("row %d Kind = %q, want %q", i, row.Kind, wantKinds[i])
		}
		if row.Label != wantLabels[i] {
			t.Errorf("row %d Label = %q, want %q", i, row.Label, wantLabels[i])
		}
	}
}

func TestBuildCalendarViewBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		entry        calendarEntry
		wantRunning  bool
		wantUpcoming bool
	}{
		{
			name:        "starts exactly now is running",
			entry:       calendarEntry{Start: atOffset(0), End: atOffset(time.Hour)},
			wantRunning: true,
		},
		{
			name:  "ends exactly now is neither",
			entry: calendarEntry{Start: atOffset(-time.Hour), End: atOffset(0)},
		},
		{
			name:         "starts exactly at the horizon is upcoming",
			entry:        calendarEntry{Start: atOffset(calendarUpcomingDays * 24 * time.Hour), End: atOffset(400 * time.Hour)},
			wantUpcoming: true,
		},
		{
			name:  "starts just past the horizon is excluded",
			entry: calendarEntry{Start: atOffset(calendarUpcomingDays*24*time.Hour + time.Millisecond), End: atOffset(400 * time.Hour)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.entry.Name = "x"
			view := buildCalendarView(&calendarData{Events: []calendarEntry{tt.entry}}, calendarNow)
			if got := len(view.Running) == 1; got != tt.wantRunning {
				t.Errorf("running = %v, want %v", got, tt.wantRunning)
			}
			if got := len(view.Upcoming) == 1; got != tt.wantUpcoming {
				t.Errorf("upcoming = %v, want %v", got, tt.wantUpcoming)
			}
		})
	}
}

func TestBuildCalendarViewEmpty(t *testing.T) {
	view := buildCalendarView(&calendarData{}, calendarNow)
	if len(view.Running) != 0 || len(view.Upcoming) != 0 {
		t.Errorf("empty schedule produced running=%d upcoming=%d", len(view.Running), len(view.Upcoming))
	}
}

// TestRenderCalendarPageEscapesNames guards the reason for using html/template:
// schedule names come from game text bundles and must never break the markup.
func TestRenderCalendarPageEscapesNames(t *testing.T) {
	s := &OctoHTTPServer{} // BaseDir empty → no override on disk → embedded template
	cal := &calendarData{
		Events: []calendarEntry{
			{Name: `Nier <script>alert(1)</script> & "friends"`, Start: atOffset(time.Hour), End: atOffset(2 * time.Hour)},
		},
	}

	page, err := s.renderCalendarPage(cal, calendarNow)
	if err != nil {
		t.Fatalf("renderCalendarPage: %v", err)
	}

	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Error("event name was not escaped — raw <script> reached the output")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Error("expected the escaped form of the event name in the output")
	}
	// Sanity: the page still rendered its structure and the row itself.
	for _, want := range []string{"Now Running", "Upcoming", `class="tag event"`, "Event"} {
		if !strings.Contains(page, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

// writeOverride drops a calendar.html into <dir>/assets/release/ and returns its path.
func writeOverride(t *testing.T, dir, body string) string {
	t.Helper()
	releaseDir := filepath.Join(dir, "assets", "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(releaseDir, calendarTemplateName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	return path
}

// TestCalendarTemplateOverride is the point of the whole design: styling and
// wording can be changed on disk and picked up without a rebuild or restart.
func TestCalendarTemplateOverride(t *testing.T) {
	dir := t.TempDir()
	path := writeOverride(t, dir, `OVERRIDE running={{len .Running}} upcoming={{len .Upcoming}}`)

	s := &OctoHTTPServer{BaseDir: dir}
	cal := &calendarData{Events: []calendarEntry{
		{Name: "live", Start: atOffset(-time.Hour), End: atOffset(time.Hour)},
	}}

	page, err := s.renderCalendarPage(cal, calendarNow)
	if err != nil {
		t.Fatalf("renderCalendarPage: %v", err)
	}
	if !strings.Contains(page, "OVERRIDE running=1 upcoming=0") {
		t.Errorf("on-disk override was not used, got: %q", page)
	}

	// Edit it. Chtimes forces a distinct mtime so the check is deterministic
	// rather than dependent on filesystem timestamp resolution.
	if err := os.WriteFile(path, []byte(`SECOND EDIT`), 0o644); err != nil {
		t.Fatalf("rewrite override: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	page, err = s.renderCalendarPage(cal, calendarNow)
	if err != nil {
		t.Fatalf("renderCalendarPage after edit: %v", err)
	}
	if !strings.Contains(page, "SECOND EDIT") {
		t.Errorf("edited override was not picked up, got: %q", page)
	}
}

// A broken edit must not take the page down.
func TestCalendarTemplateMalformedOverrideFallsBack(t *testing.T) {
	dir := t.TempDir()
	writeOverride(t, dir, `{{.Running`) // unclosed action — fails to parse

	s := &OctoHTTPServer{BaseDir: dir}
	page, err := s.renderCalendarPage(&calendarData{}, calendarNow)
	if err != nil {
		t.Fatalf("malformed override should fall back, not error: %v", err)
	}
	if !strings.Contains(page, "LUNAR TEAR") {
		t.Error("expected the embedded default template after a malformed override")
	}
}

func rowNames(rows []calendarRow) []string {
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
