package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/MelloB1989/wacli/wa/sarvam"
)

func main() {
	cfg := sarvam.Config{APIKey: os.Getenv("SARVAM_API_KEY")}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	tts, err := sarvam.DialTTS(ctx, cfg, "en-IN", "", 0)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	_ = tts.Speak(ctx, "Yeah, I hear you loud and clear. You've got a bunch of pending items: the Vector Company website revamp still needs review, and Siva has not uploaded the beta APK.")
	_ = tts.Flush(ctx)
	var all []byte
	sizes := []int{}
	deadline := time.After(12 * time.Second)
loop:
	for {
		select {
		case c, ok := <-tts.Audio():
			if !ok {
				break loop
			}
			all = append(all, c...)
			sizes = append(sizes, len(c))
		case <-deadline:
			break loop
		}
	}
	n := len(all) / 2
	var sum float64
	peak := 0.0
	clipped := 0
	for i := 0; i+1 < len(all); i += 2 {
		v := float64(int16(uint16(all[i]) | uint16(all[i+1])<<8)) / 32768
		sum += v * v
		if math.Abs(v) > peak {
			peak = math.Abs(v)
		}
		if math.Abs(v) > 0.98 {
			clipped++
		}
	}
	rms := math.Sqrt(sum / float64(n))
	fmt.Printf("chunks=%d bytes=%d seconds=%.1f\n", len(sizes), len(all), float64(n)/16000)
	fmt.Printf("rms=%.3f peak=%.3f clipped=%.2f%%\n", rms, peak, float64(clipped)*100/float64(n))
	partial := 0
	for _, s := range sizes {
		if s%1920 != 0 {
			partial++
		}
	}
	fmt.Printf("chunks not a whole 60ms frame: %d/%d (each loses its tail with PushFrames)\n", partial, len(sizes))
	first := sizes
	if len(first) > 8 {
		first = first[:8]
	}
	fmt.Println("first chunk sizes:", first)
	os.WriteFile("/tmp/claude-1000/tts-out.raw", all, 0600)
}
