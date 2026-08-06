package deepgram

import "testing"

func TestLiveListenCostUSD_Nova3Language(t *testing.T) {
	t.Parallel()

	const seconds = 78.8

	mono, ok := liveListenCostUSD("nova-3", seconds, "ar")
	if !ok {
		t.Fatal("expected nova-3 monolingual rate to be known")
	}
	wantMono := (seconds / 60.0) * nova3LiveMonolingualUSDPerMin // ~$0.0101
	if diff := mono - wantMono; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("monolingual cost = %v, want %v", mono, wantMono)
	}

	multiEmpty, ok := liveListenCostUSD("nova-3", seconds, "")
	if !ok {
		t.Fatal("expected nova-3 multilingual rate to be known")
	}
	wantMulti := (seconds / 60.0) * nova3LiveMultilingualUSDPerMin // ~$0.0121
	if diff := multiEmpty - wantMulti; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("multilingual (empty lang) cost = %v, want %v", multiEmpty, wantMulti)
	}

	multiExplicit, ok := liveListenCostUSD("nova-3-general", seconds, "multi")
	if !ok {
		t.Fatal("expected nova-3-general multilingual rate to be known")
	}
	if diff := multiExplicit - wantMulti; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("multilingual (multi) cost = %v, want %v", multiExplicit, wantMulti)
	}
}

func TestLiveListenCostUSD_UnknownModelFallsBack(t *testing.T) {
	t.Parallel()
	if _, ok := liveListenCostUSD("whisper", 30, "en"); ok {
		t.Fatal("whisper should fall back to datasheet pricing")
	}
}

func TestIsLiveMultilingualLanguage(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"":             true,
		"multi":        true,
		"Multilingual": true,
		"ar":           false,
		"en":           false,
		"en-US":        false,
	}
	for in, want := range cases {
		if got := isLiveMultilingualLanguage(in); got != want {
			t.Fatalf("isLiveMultilingualLanguage(%q)=%v, want %v", in, got, want)
		}
	}
}
