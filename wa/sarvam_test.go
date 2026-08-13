package wa

import "testing"

func TestSarvamLanguageFollowsTheScript(t *testing.T) {
	// Getting this wrong mispronounces the whole call, and the caller should not
	// have to declare a language they did not think about.
	for text, want := range map[string]string{
		"Hello, are you free?":       "en-IN",
		"नमस्ते, आप कैसे हैं":         "hi-IN",
		"హలో ఎలా ఉన్నారు":            "te-IN",
		"வணக்கம்":                    "ta-IN",
		"bhai kal call karte hain":   "en-IN", // Hinglish in Latin script
	} {
		if got := sarvamLanguage(text); got != want {
			t.Errorf("%q -> %s, want %s", text, got, want)
		}
	}
}

func TestSynthesiseWithSarvamNeedsAKey(t *testing.T) {
	t.Setenv("SARVAM_API_KEY", "")
	if sarvamConfigured() {
		t.Fatal("no key should read as unconfigured")
	}
	if _, err := synthesiseWithSarvam("hello", ""); err == nil {
		t.Error("expected an error without a key")
	}
}

func TestSynthesiseWithSarvamRefusesEmptyText(t *testing.T) {
	t.Setenv("SARVAM_API_KEY", "test-key")
	if _, err := synthesiseWithSarvam("   ", ""); err == nil {
		t.Error("empty text must not reach the API")
	}
}
