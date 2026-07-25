package masterdata

import (
	"testing"

	"lunar-tear/server/internal/model"
)

func TestEnvPrice(t *testing.T) {
	const def int32 = 300

	tests := []struct {
		name string
		set  bool
		val  string
		want int32
	}{
		{"unset falls back to the shipped price", false, "", def},
		{"empty falls back", true, "", def},
		{"whitespace falls back", true, "   ", def},
		{"valid override", true, "30", 30},
		{"whitespace is trimmed", true, "  45  ", 45},
		{"zero is allowed (free pull)", true, "0", 0},
		{"negative falls back", true, "-5", def},
		{"garbage falls back", true, "cheap", def},
		{"float falls back", true, "12.5", def},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const key = "LUNAR_TEST_GACHA_PRICE"
			if tt.set {
				t.Setenv(key, tt.val)
			}
			if got := envPrice(key, def); got != tt.want {
				t.Errorf("envPrice(%q) = %d, want %d", tt.val, got, tt.want)
			}
		})
	}
}

// The strike-through price must stay at the shipped constant so an overridden
// price reads as a real discount in-game rather than "300 slashed through, 300
// to pay".
func TestBasicPricePhasesKeepShippedRegularPrice(t *testing.T) {
	phases := buildPremiumBasicPricePhases(1)
	if len(phases) < 2 {
		t.Fatalf("got %d phases, want at least 2", len(phases))
	}

	single, multi := phases[0], phases[1]
	if single.RegularPrice != model.PremiumSinglePullPrice {
		t.Errorf("single RegularPrice = %d, want the shipped %d", single.RegularPrice, model.PremiumSinglePullPrice)
	}
	if multi.RegularPrice != model.PremiumMultiPullPrice {
		t.Errorf("multi RegularPrice = %d, want the shipped %d", multi.RegularPrice, model.PremiumMultiPullPrice)
	}
	if single.Price != premiumSinglePullPrice {
		t.Errorf("single Price = %d, want the configured %d", single.Price, premiumSinglePullPrice)
	}
	if multi.Price != premiumMultiPullPrice {
		t.Errorf("multi Price = %d, want the configured %d", multi.Price, premiumMultiPullPrice)
	}
}

// Ticket-priced phases are item counts, not gems, and must never be scaled.
func TestTicketPhasesAreNotPriced(t *testing.T) {
	for _, p := range buildPremiumBasicPricePhases(1) {
		if p.PriceType != model.PriceTypeConsumableItem {
			continue
		}
		if p.Price != 1 || p.RegularPrice != 1 {
			t.Errorf("ticket phase price = %d/%d, want 1/1", p.Price, p.RegularPrice)
		}
	}
	for _, p := range buildChapterPricePhases(1) {
		if p.PriceType != model.PriceTypeConsumableItem {
			t.Errorf("chapter phase should be ticket-priced, got price type %d", p.PriceType)
		}
		if p.Price != p.RegularPrice {
			t.Errorf("chapter ticket phase price %d != regular %d", p.Price, p.RegularPrice)
		}
	}
}

// Step-up phases already strike through the full multi-pull price; that must
// stay the shipped constant too.
func TestStepUpPhasesStrikeThroughShippedMultiPrice(t *testing.T) {
	phases := buildStepUpPricePhases(1, 5)
	if len(phases) == 0 {
		t.Fatal("no step-up phases built")
	}
	for i, p := range phases {
		if p.RegularPrice != model.PremiumMultiPullPrice {
			t.Errorf("step %d RegularPrice = %d, want the shipped %d", i+1, p.RegularPrice, model.PremiumMultiPullPrice)
		}
	}
	// A free step must be charged as regular gems, not paid gems.
	if phases[1].Price != 0 {
		t.Errorf("step 2 Price = %d, want 0 (free step)", phases[1].Price)
	}
	if phases[1].PriceType != model.PriceTypeGem {
		t.Errorf("free step PriceType = %d, want PriceTypeGem (%d)", phases[1].PriceType, model.PriceTypeGem)
	}
}
