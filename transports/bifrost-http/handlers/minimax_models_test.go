package handlers

import (
	"reflect"
	"testing"
)

func TestCurrentMinimaxSuggestions(t *testing.T) {
	models := []string{"MiniMax-M2.7", "speech-02-hd", "MiniMax-M3", "MiniMax-Text-01", "MiniMax-M3.1-Flash", "video-01", "custom-model", "abab6.5-chat", "MiniMax-M2.5"}
	original := append([]string(nil), models...)
	want := []string{"MiniMax-M3.1-Flash", "MiniMax-M3", "speech-02-hd", "video-01", "custom-model"}
	if got := currentMinimaxSuggestions(models); !reflect.DeepEqual(got, want) {
		t.Fatalf("suggestions = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(models, original) {
		t.Fatal("cached catalog was mutated")
	}
	qualified := []string{"minimax/MiniMax-M2.7", "minimax/MiniMax-M3", "minimax/MiniMax-M3.1-Flash", "minimax/speech-02-hd"}
	wantQualified := []string{"minimax/MiniMax-M3.1-Flash", "minimax/MiniMax-M3", "minimax/speech-02-hd"}
	if got := currentMinimaxSuggestions(qualified); !reflect.DeepEqual(got, wantQualified) {
		t.Fatalf("qualified suggestions = %v, want %v", got, wantQualified)
	}
}
