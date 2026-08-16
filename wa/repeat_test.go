package wa

import "testing"

// The guard exists because three independent senders above wacli each thought
// they were the one answering, and one chat got the same sentence three times
// in fourteen minutes. What matters is that wording variance does not defeat
// it, and that short conversational replies stay free to repeat.
func TestNormalizeSendTextIgnoresSpacingAndCase(t *testing.T) {
	a := normalizeSendText("done, reminder set for Kartik  — setup Linux 🐧✅")
	b := normalizeSendText("Done, reminder set for Kartik — setup Linux 🐧✅\n")
	if a != b {
		t.Errorf("the same message with different spacing/case must compare equal:\n %q\n %q", a, b)
	}
	if normalizeSendText("  ") != "" {
		t.Error("blank text must normalise to empty so it is never treated as a repeat")
	}
	if normalizeSendText("one two") == normalizeSendText("one three") {
		t.Error("different messages must not collide")
	}
}
