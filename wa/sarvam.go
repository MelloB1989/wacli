package wa

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Speech for calls, from a service rather than the operating system.
//
// synthesiseSpeech shells out to macOS `say` and `afconvert`, which means a
// call that speaks works on the author's laptop and nowhere else — on the Linux
// box this daemon actually runs on, --say fails outright. Sarvam works
// anywhere, and speaks the languages this account's calls are actually in.
//
// It returns exactly what the call pipeline wants — 16 kHz mono 16-bit WAV — so
// there is no conversion step to get wrong.

const (
	sarvamTTSURL  = "https://api.sarvam.ai/text-to-speech"
	sarvamModel   = "bulbul:v2"
	sarvamTimeout = 45 * time.Second
	// The call pipeline's format. Asked of Sarvam directly rather than
	// resampled afterwards.
	sarvamSampleRate = 16000
)

// sarvamConfigured reports whether speech can be synthesised without macOS.
func sarvamConfigured() bool { return strings.TrimSpace(os.Getenv("SARVAM_API_KEY")) != "" }

// synthesiseWithSarvam turns text into a WAV file and returns its path.
//
// voice selects the speaker; empty takes a default. The language code follows
// the text's own script when it is obvious and falls back to Indian English,
// which is the right default for this account and wrong for nobody.
func synthesiseWithSarvam(text, voice string) (string, error) {
	key := strings.TrimSpace(os.Getenv("SARVAM_API_KEY"))
	if key == "" {
		return "", fmt.Errorf("SARVAM_API_KEY is not set")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("nothing to say")
	}
	if voice = strings.TrimSpace(voice); voice == "" {
		voice = "anushka"
	}

	payload, err := json.Marshal(map[string]any{
		"text":                 text,
		"target_language_code": sarvamLanguage(text),
		"speaker":              voice,
		"model":                sarvamModel,
		"speech_sample_rate":   sarvamSampleRate,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, sarvamTTSURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("api-subscription-key", key)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: sarvamTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sarvam: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Sarvam explains a rejected request in its body; the status alone does
		// not say whether the key is wrong or the speaker name is.
		return "", fmt.Errorf("sarvam refused the request (%s): %.200s", resp.Status, body)
	}

	var out struct {
		Audios []string `json:"audios"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("sarvam: could not read the response: %w", err)
	}
	if len(out.Audios) == 0 {
		return "", fmt.Errorf("sarvam returned no audio")
	}
	raw, err := base64.StdEncoding.DecodeString(out.Audios[0])
	if err != nil {
		return "", fmt.Errorf("sarvam: the audio was not valid base64: %w", err)
	}

	f, err := os.CreateTemp("", "wacli-sarvam-*.wav")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// sarvamLanguage picks the language code from the text's script.
//
// Devanagari is Hindi far more often than not on this account, and Telugu and
// Tamil have their own blocks — enough to get the pronunciation right without
// asking the caller to declare a language they did not think about. Anything
// else is Indian English, which also reads Hinglish written in Latin script
// correctly, that being how most of these messages are actually written.
func sarvamLanguage(text string) string {
	for _, r := range text {
		switch {
		case r >= 0x0900 && r <= 0x097F:
			return "hi-IN"
		case r >= 0x0C00 && r <= 0x0C7F:
			return "te-IN"
		case r >= 0x0B80 && r <= 0x0BFF:
			return "ta-IN"
		case r >= 0x0980 && r <= 0x09FF:
			return "bn-IN"
		case r >= 0x0A80 && r <= 0x0AFF:
			return "gu-IN"
		case r >= 0x0C80 && r <= 0x0CFF:
			return "kn-IN"
		case r >= 0x0D00 && r <= 0x0D7F:
			return "ml-IN"
		}
	}
	return "en-IN"
}
