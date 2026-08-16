// Streams a raw s16le 16k mono file into Sarvam transcription, so captured call
// audio can be judged by the same service the call uses.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/MelloB1989/wacli/wa/sarvam"
)

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	cfg := sarvam.Config{APIKey: os.Getenv("SARVAM_API_KEY")}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stt, err := sarvam.DialSTT(ctx, cfg, "en-IN")
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	go func() {
		for ev := range stt.Events() {
			if ev.Kind == sarvam.Transcript && ev.Text != "" {
				fmt.Printf("TRANSCRIPT final=%v %q\n", ev.Final, ev.Text)
			}
		}
	}()
	const chunk = 1920 // 60ms
	for i := 0; i < len(raw); i += chunk {
		end := i + chunk
		if end > len(raw) {
			end = len(raw)
		}
		if err := stt.Send(ctx, raw[i:end]); err != nil {
			fmt.Println("send:", err)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("sent %d bytes; waiting for transcripts\n", len(raw))
	time.Sleep(10 * time.Second)
}
