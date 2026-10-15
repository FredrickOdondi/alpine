// Command evals runs the agent eval fixtures.
//
//	go run ./cmd/evals                 # replay recorded responses (no network)
//	go run ./cmd/evals -live           # run against the configured gateway (LLM_BASE_URL, LLM_API_KEY)
//	go run ./cmd/evals -live -update   # ...and refresh the recordings
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/alpine-dev/alpine/packages/agents/evals"
	"github.com/alpine-dev/alpine/packages/shared/config"
	"github.com/alpine-dev/alpine/packages/shared/llm"
)

func main() {
	dir := flag.String("dir", "evals/fixtures", "fixtures directory")
	live := flag.Bool("live", false, "call the real gateway instead of replaying recordings")
	update := flag.Bool("update", false, "with -live: write new recordings into the fixtures")
	flag.Parse()

	fixtures, err := evals.Load(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx := context.Background()
	failed := 0
	for _, f := range fixtures {
		var gw *llm.Gateway
		var calls func() int
		var rec *evals.RecordingProvider
		if *live {
			cfg, err := config.LoadLLM()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			rec = &evals.RecordingProvider{Inner: &llm.OpenAICompatible{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, BYOKInBody: cfg.BYOKInBody}}
			gw, err = llm.NewGateway(llm.Options{Provider: rec, Routing: cfg.Routing, Retry: cfg.Retry})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			calls = func() int { return len(rec.Recs) }
		} else {
			var p *evals.ReplayProvider
			gw, p, err = evals.ReplayGateway(f, evals.DefaultRouting())
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			calls = func() int { return len(p.Calls) }
		}
		res := evals.Run(ctx, f, gw, calls)
		status := "PASS"
		if !res.Pass {
			status, failed = "FAIL", failed+1
		}
		fmt.Printf("%-4s %-10s %-36s calls=%d cost=$%.5f\n", status, res.Stage, res.Name, res.Calls, res.CostUSD)
		for _, m := range res.Failures {
			fmt.Printf("       - %s\n", m)
		}
		if *live && *update && rec != nil && len(rec.Recs) > 0 {
			f.Recorded = rec.Recs
			if err := f.Save(); err != nil {
				fmt.Fprintln(os.Stderr, "save:", err)
			}
		}
	}
	fmt.Printf("\n%d/%d passed\n", len(fixtures)-failed, len(fixtures))
	if failed > 0 {
		os.Exit(1)
	}
}
