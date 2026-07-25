package gametime

import (
	"testing"
	"time"
)

func TestStartOfDayPSTMillisAt(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want time.Time // 00:00 PST expressed in UTC (PST is UTC-8, so 08:00 UTC)
	}{
		{
			name: "midday UTC maps to same PST day",
			now:  time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
			want: time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC),
		},
		{
			name: "early UTC still belongs to the previous PST day",
			now:  time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC),
			want: time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC),
		},
		{
			name: "exactly at the boundary is the start of that day",
			now:  time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC),
			want: time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC),
		},
		{
			name: "one milli before the boundary is the previous day",
			now:  time.Date(2026, 7, 15, 7, 59, 59, 999e6, time.UTC),
			want: time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StartOfDayPSTMillisAt(tt.now.UnixMilli())
			if got != tt.want.UnixMilli() {
				t.Errorf("StartOfDayPSTMillisAt(%s) = %s, want %s",
					tt.now.Format(time.RFC3339),
					time.UnixMilli(got).UTC().Format(time.RFC3339),
					tt.want.Format(time.RFC3339))
			}
		})
	}
}
