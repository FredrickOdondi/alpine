package gates

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

var white = color.NRGBA{255, 255, 255, 255}

func TestPixelDiff(t *testing.T) {
	a := solid(10, 10, white)
	if d := PixelDiff(a, solid(10, 10, white), 0); d.DiffPixels != 0 || d.Pct != 0 || d.TotalPixels != 100 {
		t.Errorf("identical: %+v", d)
	}
	b := solid(10, 10, white)
	b.SetNRGBA(3, 4, color.NRGBA{0, 0, 0, 255})
	b.SetNRGBA(5, 5, color.NRGBA{250, 250, 250, 255}) // anti-aliasing noise
	d := PixelDiff(a, b, 0.1)
	if d.DiffPixels != 1 || d.Pct != 1 {
		t.Errorf("one changed pixel within tolerance noise: %+v", d)
	}
	if got := d.Image.NRGBAAt(3, 4); got != (color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("changed pixel not highlighted: %v", got)
	}
	if got := d.Image.NRGBAAt(0, 0); got.R != got.G || got.R == 0 {
		t.Errorf("unchanged pixel should be faded gray: %v", got)
	}
	if d := PixelDiff(a, b, 0); d.DiffPixels != 2 {
		t.Errorf("zero tolerance: %+v", d)
	}
	// Size change: the extra rows count as different.
	if d := PixelDiff(a, solid(10, 12, white), 0); d.Width != 10 || d.Height != 12 || d.DiffPixels != 20 {
		t.Errorf("size mismatch: %+v", d)
	}
	if d := PixelDiff(image.NewNRGBA(image.Rect(0, 0, 0, 0)), image.NewNRGBA(image.Rect(0, 0, 0, 0)), 0); d.Pct != 0 {
		t.Errorf("empty: %+v", d)
	}
}

// fakeShooter renders a white page; "after" screenshots of routes in changed
// get a black block covering pct percent of the rows.
type fakeShooter struct {
	mu      sync.Mutex
	changed map[string]int
	fail    string
	reqs    []ScreenshotRequest
}

func (f *fakeShooter) Screenshot(_ context.Context, req ScreenshotRequest) ([]byte, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if req.Route == f.fail {
		return nil, errors.New("page crashed")
	}
	img := solid(100, 100, white)
	if req.Side == "after" {
		for y := 0; y < f.changed[req.Route]; y++ {
			for x := 0; x < 100; x++ {
				img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes(), nil
}

func visualGate(v *VisualConfig, routes string) GateConfig {
	return GateConfig{Spec: gate.Spec{ID: "visual", Routes: routes}, Type: TypeVisual, Visual: v}
}

func runVisualGate(t *testing.T, r *Runner, g GateConfig, routes []string) GateResult {
	t.Helper()
	cfg := mustConfig(t, Config{Gates: []GateConfig{g}})
	res, err := r.Run(context.Background(), RunRequest{JobID: t.Name(), RepoDir: t.TempDir(), BaseRef: "main", Config: cfg, VisualRoutes: routes})
	if err != nil {
		t.Fatal(err)
	}
	return res.Gates[0]
}

func TestVisualGate(t *testing.T) {
	shooter := &fakeShooter{changed: map[string]int{"/settings": 5}}
	r := NewRunner(nil)
	r.Screenshotter = shooter
	r.ArtifactsRoot = t.TempDir()
	v := &VisualConfig{BeforeURL: "http://base.preview.local", AfterURL: "http://head.preview.local/", Routes: []string{"/", "/settings"},
		ThresholdPct: ptr(1.0), ArtifactsDir: "shots"}

	gr := runVisualGate(t, r, visualGate(v, ""), nil)
	if gr.Status != StatusFail || countBySeverity(gr.Findings)[SeverityError] != 1 {
		t.Fatalf("5%% change on /settings should fail at 1%%: %+v", gr)
	}
	if !strings.Contains(gr.Feedback, "/settings") || !strings.Contains(gr.Feedback, "5.00% of pixels changed") {
		t.Errorf("feedback: %s", gr.Feedback)
	}
	want := []string{
		"shots/visual/01-root.before.png", "shots/visual/01-root.after.png", "shots/visual/01-root.diff.png",
		"shots/visual/02-settings.before.png", "shots/visual/02-settings.after.png", "shots/visual/02-settings.diff.png",
	}
	if strings.Join(gr.ArtifactKeys, ",") != strings.Join(want, ",") {
		t.Errorf("artifact keys = %v", gr.ArtifactKeys)
	}
	for _, k := range want {
		b, err := os.ReadFile(filepath.Join(r.ArtifactsRoot, k))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := png.Decode(bytes.NewReader(b)); err != nil {
			t.Errorf("%s is not a PNG: %v", k, err)
		}
	}
	urls := []string{}
	for _, q := range shooter.reqs {
		urls = append(urls, q.Side+" "+q.URL)
		if q.Viewport != (Viewport{1280, 720}) {
			t.Errorf("default viewport not applied: %+v", q.Viewport)
		}
	}
	if got := strings.Join(urls, ","); got != "before http://base.preview.local/,after http://head.preview.local/,before http://base.preview.local/settings,after http://head.preview.local/settings" {
		t.Errorf("urls = %s", got)
	}

	// Under the threshold passes, with the diff reported as info.
	v.ThresholdPct = ptr(10.0)
	if gr := runVisualGate(t, r, visualGate(v, ""), nil); gr.Status != StatusPass || countBySeverity(gr.Findings)[SeverityInfo] != 2 {
		t.Errorf("under threshold: %+v", gr)
	}
	// warn_only turns a failure into a non-blocking warn.
	v.ThresholdPct = ptr(1.0)
	g := visualGate(v, "")
	g.WarnOnly = true
	if gr := runVisualGate(t, r, g, nil); gr.Status != StatusWarn {
		t.Errorf("warn_only: %+v", gr)
	}
}

func TestVisualGateRoutesAndSkips(t *testing.T) {
	shooter := &fakeShooter{}
	r := NewRunner(nil)
	r.Screenshotter = shooter
	r.TempDir = t.TempDir()
	v := &VisualConfig{BeforeURL: "http://a", AfterURL: "http://b"}

	// routes: auto takes the orchestrator's routes (changed pages).
	gr := runVisualGate(t, r, visualGate(v, "auto"), []string{"/billing", "not-a-route", "/"})
	if gr.Status != StatusPass || len(gr.Findings) != 2 || len(gr.ArtifactKeys) != 6 {
		t.Errorf("auto routes: %+v", gr)
	}
	// Without ArtifactsRoot, artifacts land in a temp dir outside the repo.
	if ents, _ := os.ReadDir(r.TempDir); len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "alpine-artifacts-") {
		t.Errorf("artifacts root: %v", ents)
	}
	// Comma-separated routes in the shared Spec.Routes string.
	if gr := runVisualGate(t, r, visualGate(v, "/a, /b"), nil); len(gr.Findings) != 2 {
		t.Errorf("comma routes: %+v", gr)
	}
	if gr := runVisualGate(t, r, visualGate(v, "auto"), nil); gr.Status != StatusSkipped {
		t.Errorf("no routes should skip: %+v", gr)
	}
	// Jeff's example config: a visual gate with only routes: auto.
	if gr := runVisualGate(t, r, visualGate(nil, "auto"), []string{"/"}); gr.Status != StatusSkipped || !strings.Contains(gr.Summary, "not configured") {
		t.Errorf("unconfigured visual gate: %+v", gr)
	}
	shooter.fail = "/x"
	if gr := runVisualGate(t, r, visualGate(v, "/x"), nil); gr.Status != StatusError || !hasRule(gr.Findings, "screenshot") {
		t.Errorf("screenshot failure: %+v", gr)
	}
}

func TestVisualConfigValidation(t *testing.T) {
	good := "gates:\n  - id: visual\n    type: visual\n    visual:\n      before_url: http://localhost:3001\n      after_url: http://localhost:3002\n      routes: [/, /settings]\n      threshold_pct: 0.5\n      pixel_tolerance: 0.2\n      viewport: {width: 800, height: 600}\n      artifacts_dir: out/visual\n"
	if _, err := ParseConfig([]byte(good)); err != nil {
		t.Fatalf("good config: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(good, "http://localhost:3001", "file:///etc", 1),
		strings.Replace(good, "[/, /settings]", "[settings]", 1),
		strings.Replace(good, "threshold_pct: 0.5", "threshold_pct: 101", 1),
		strings.Replace(good, "pixel_tolerance: 0.2", "pixel_tolerance: 2", 1),
		strings.Replace(good, "width: 800", "width: -1", 1),
		strings.Replace(good, "out/visual", "../escape", 1),
		strings.Replace(good, "type: visual", "type: lint", 1),
		strings.Replace(good, "threshold_pct", "threshold", 1),
	} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("expected error for:\n%s", bad)
		}
	}
}

func TestCommandScreenshotter(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	_ = png.Encode(&buf, solid(2, 2, white))
	fixture := filepath.Join(dir, "fixture.png")
	if err := os.WriteFile(fixture, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	c := CommandScreenshotter{TempDir: t.TempDir()}
	req := ScreenshotRequest{Side: "after", URL: "http://x/y", Viewport: Viewport{800, 600}, FullPage: true, Dir: dir,
		Env:     []string{"PATH=" + os.Getenv("PATH"), "FIXTURE=" + fixture},
		Command: `test "$ALPINE_URL" = http://x/y && test "$ALPINE_VIEWPORT" = 800,600 && test "$ALPINE_FULL_PAGE" = 1 && test "$ALPINE_SIDE" = after && cp "$FIXTURE" "$ALPINE_OUT"`}
	b, err := c.Screenshot(context.Background(), req)
	if err != nil || !bytes.Equal(b, buf.Bytes()) {
		t.Fatalf("screenshot: %v", err)
	}
	req.Command = "echo no browser >&2; exit 1"
	if _, err := c.Screenshot(context.Background(), req); err == nil || !strings.Contains(err.Error(), "no browser") {
		t.Errorf("want command failure, got %v", err)
	}
	if ents, _ := os.ReadDir(c.TempDir); len(ents) != 0 {
		t.Errorf("temp screenshots left behind: %v", ents)
	}
}
