package gates

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DefaultScreenshotCommand takes one screenshot with the Playwright CLI. It
// runs with `sh -c` in the head repo, so Playwright must be installed there.
const DefaultScreenshotCommand = `npx --no-install playwright screenshot --viewport-size="$ALPINE_VIEWPORT" ${ALPINE_FULL_PAGE:+--full-page} "$ALPINE_URL" "$ALPINE_OUT"`

// Visual gate defaults.
const (
	DefaultVisualThresholdPct   = 0.1 // % of pixels allowed to differ per route
	DefaultVisualPixelTolerance = 0.1 // per-channel tolerance (fraction of 255)
)

// VisualConfig configures a type: visual gate.
//
//   - id: visual
//     type: visual
//     visual:
//     before_url: http://base.preview.local:3000   # app built from the base SHA
//     after_url: http://head.preview.local:3000    # app built from the agent's head
//     routes: [/, /settings]                       # or Spec routes: auto (+ RunRequest.VisualRoutes)
//     threshold_pct: 0.1
//     pixel_tolerance: 0.1
//     viewport: {width: 1280, height: 720}
//     full_page: true
//     artifacts_dir: visual                        # under Runner.ArtifactsRoot
type VisualConfig struct {
	BeforeURL         string   `yaml:"before_url" json:"beforeUrl"`
	AfterURL          string   `yaml:"after_url" json:"afterUrl"`
	Routes            []string `yaml:"routes" json:"routes,omitempty"`
	ThresholdPct      *float64 `yaml:"threshold_pct" json:"thresholdPct,omitempty"`
	PixelTolerance    *float64 `yaml:"pixel_tolerance" json:"pixelTolerance,omitempty"`
	Viewport          Viewport `yaml:"viewport" json:"viewport"`
	FullPage          bool     `yaml:"full_page" json:"fullPage,omitempty"`
	ArtifactsDir      string   `yaml:"artifacts_dir" json:"artifactsDir,omitempty"`
	ScreenshotCommand string   `yaml:"screenshot_command" json:"screenshotCommand,omitempty"`
}

// Viewport size in CSS pixels (default 1280x720).
type Viewport struct {
	Width  int `yaml:"width" json:"width"`
	Height int `yaml:"height" json:"height"`
}

func (v *VisualConfig) threshold() float64 {
	if v.ThresholdPct != nil {
		return *v.ThresholdPct
	}
	return DefaultVisualThresholdPct
}

func (v *VisualConfig) tolerance() float64 {
	if v.PixelTolerance != nil {
		return *v.PixelTolerance
	}
	return DefaultVisualPixelTolerance
}

func (v *VisualConfig) viewport() Viewport {
	vp := v.Viewport
	if vp.Width <= 0 {
		vp.Width = 1280
	}
	if vp.Height <= 0 {
		vp.Height = 720
	}
	return vp
}

func (v *VisualConfig) validate() []string {
	var errs []string
	for name, u := range map[string]string{"before_url": v.BeforeURL, "after_url": v.AfterURL} {
		pu, err := url.Parse(u)
		if u == "" || err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			errs = append(errs, name+" must be an http(s) URL")
		}
	}
	for _, r := range v.Routes {
		if !strings.HasPrefix(r, "/") {
			errs = append(errs, fmt.Sprintf("route %q must start with /", r))
		}
	}
	if t := v.threshold(); t < 0 || t > 100 {
		errs = append(errs, "threshold_pct must be within 0..100")
	}
	if t := v.tolerance(); t < 0 || t > 1 {
		errs = append(errs, "pixel_tolerance must be within 0..1")
	}
	if v.Viewport.Width < 0 || v.Viewport.Height < 0 || v.Viewport.Width > 10000 || v.Viewport.Height > 10000 {
		errs = append(errs, "viewport must be within 1..10000")
	}
	if err := checkRelPath(v.ArtifactsDir); err != nil {
		errs = append(errs, "artifacts_dir: "+err.Error())
	}
	sortStrings(errs)
	return errs
}

// ScreenshotRequest asks for one PNG screenshot.
type ScreenshotRequest struct {
	GateID   string
	Side     string // "before" (base) or "after" (head)
	URL      string // full URL: before_url/after_url + route
	Route    string
	Viewport Viewport
	FullPage bool
	// Dir is the head repo (working dir for command-based screenshotters).
	Dir string
	// Env is the gate's allowlisted environment.
	Env []string
	// Command is visual.screenshot_command (or DefaultScreenshotCommand).
	Command string
}

// Screenshotter takes screenshots for the visual gate.
type Screenshotter interface {
	Screenshot(ctx context.Context, req ScreenshotRequest) ([]byte, error)
}

// CommandScreenshotter runs req.Command with `sh -c` in req.Dir with
// ALPINE_URL, ALPINE_OUT, ALPINE_VIEWPORT ("W,H"), ALPINE_FULL_PAGE and
// ALPINE_SIDE set, and reads the PNG written to ALPINE_OUT.
type CommandScreenshotter struct {
	// TempDir is where ALPINE_OUT files are created ("" = os.TempDir).
	TempDir string
}

// Screenshot implements Screenshotter.
func (c CommandScreenshotter) Screenshot(ctx context.Context, req ScreenshotRequest) ([]byte, error) {
	out, err := os.CreateTemp(c.TempDir, "alpine-shot-*.png")
	if err != nil {
		return nil, err
	}
	path := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(path) }()
	_ = os.Remove(path) // the tool must create it
	full := ""
	if req.FullPage {
		full = "1"
	}
	env := append(append([]string(nil), req.Env...),
		"ALPINE_URL="+req.URL, "ALPINE_OUT="+path, "ALPINE_SIDE="+req.Side, "ALPINE_FULL_PAGE="+full,
		"ALPINE_VIEWPORT="+strconv.Itoa(req.Viewport.Width)+","+strconv.Itoa(req.Viewport.Height))
	cmd := commandContext(ctx, "sh", "-c", req.Command)
	cmd.Dir, cmd.Env = req.Dir, env
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("screenshot command failed (%v): %s", err, firstLines(stderr.String(), 10))
	}
	return os.ReadFile(path)
}

var slugRE = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func routeSlug(i int, route string) string {
	s := strings.Trim(slugRE.ReplaceAllString(route, "-"), "-")
	if s == "" {
		s = "root"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return fmt.Sprintf("%02d-%s", i+1, s)
}

// visualRoutes resolves the routes for a visual gate.
func (rs *runState) visualRoutesFor(g GateConfig) []string {
	if len(g.Visual.Routes) > 0 {
		return g.Visual.Routes
	}
	if g.Routes == "auto" {
		var out []string
		for _, r := range rs.visualRoutes {
			if strings.HasPrefix(r, "/") {
				out = append(out, r)
			}
		}
		return out
	}
	var out []string
	for _, r := range strings.Split(g.Routes, ",") {
		if r = strings.TrimSpace(r); strings.HasPrefix(r, "/") {
			out = append(out, r)
		}
	}
	return out
}

func (rs *runState) artifactsDir(g GateConfig) (string, string, error) {
	rs.artifactsOnce.Do(func() {
		if rs.r.ArtifactsRoot != "" {
			rs.artifactsRoot = rs.r.ArtifactsRoot
			return
		}
		rs.artifactsRoot, rs.artifactsErr = os.MkdirTemp(rs.r.TempDir, "alpine-artifacts-")
	})
	if rs.artifactsErr != nil {
		return "", "", rs.artifactsErr
	}
	rel := g.Visual.ArtifactsDir
	if rel == "" {
		rel = "visual"
	}
	rel = filepath.ToSlash(filepath.Join(rel, slugRE.ReplaceAllString(g.ID, "-")))
	dir := filepath.Join(rs.artifactsRoot, filepath.FromSlash(rel))
	return dir, rel, os.MkdirAll(dir, 0o755)
}

// runVisual screenshots every route before (base) and after (head), diffs
// them and writes before/after/diff PNGs as artifacts.
func (rs *runState) runVisual(ctx context.Context, g GateConfig, gr GateResult) GateResult {
	if g.Visual == nil {
		gr.Status, gr.Summary = StatusSkipped, "visual gate not configured (set visual.before_url and visual.after_url)"
		return gr
	}
	v := g.Visual
	routes := rs.visualRoutesFor(g)
	if len(routes) == 0 {
		gr.Status, gr.Summary = StatusSkipped, "no routes to compare"
		return gr
	}
	dir, rel, err := rs.artifactsDir(g)
	if err != nil {
		return errorResult(gr, "artifacts", "cannot create artifacts dir: "+err.Error())
	}
	shooter := rs.r.Screenshotter
	if shooter == nil {
		shooter = CommandScreenshotter{TempDir: rs.r.TempDir}
	}
	command := v.ScreenshotCommand
	if command == "" {
		command = DefaultScreenshotCommand
	}
	gctx, cancel := context.WithTimeout(ctx, g.EffectiveTimeout())
	defer cancel()
	env := rs.r.gateEnv(rs.cfg, g, rs.baseRef, "head")
	threshold, tolerance := v.threshold(), v.tolerance()
	failedRoutes := 0
	start := timeNow()
	for i, route := range routes {
		slug := routeSlug(i, route)
		imgs := map[string]image.Image{}
		for _, side := range []string{"before", "after"} {
			base := v.BeforeURL
			if side == "after" {
				base = v.AfterURL
			}
			req := ScreenshotRequest{GateID: g.ID, Side: side, URL: strings.TrimRight(base, "/") + route, Route: route,
				Viewport: v.viewport(), FullPage: v.FullPage, Dir: rs.repo, Env: env, Command: command}
			b, err := shooter.Screenshot(gctx, req)
			if err == nil {
				err = os.WriteFile(filepath.Join(dir, slug+"."+side+".png"), b, 0o644)
			}
			var img image.Image
			if err == nil {
				img, err = png.Decode(bytes.NewReader(b))
			}
			if err != nil {
				gr.Duration = timeNow().Sub(start)
				if gctx.Err() != nil {
					gr.TimedOut = true
					gr.Status = StatusFail
					gr.Summary = "timed out (limit " + g.EffectiveTimeout().String() + ")"
					gr.Findings = append(gr.Findings, Finding{File: route, Rule: "timeout", Severity: SeverityError,
						Message: fmt.Sprintf("visual gate exceeded its timeout taking the %s screenshot", side)})
					return applyWarnOnly(gr, g)
				}
				return errorResult(gr, "screenshot", fmt.Sprintf("%s screenshot of %s failed: %v", side, route, err))
			}
			imgs[side] = img
			gr.ArtifactKeys = append(gr.ArtifactKeys, rel+"/"+slug+"."+side+".png")
		}
		d := PixelDiff(imgs["before"], imgs["after"], tolerance)
		var buf bytes.Buffer
		if err := png.Encode(&buf, d.Image); err == nil {
			if err := os.WriteFile(filepath.Join(dir, slug+".diff.png"), buf.Bytes(), 0o644); err == nil {
				gr.ArtifactKeys = append(gr.ArtifactKeys, rel+"/"+slug+".diff.png")
			}
		}
		f := Finding{File: route, Rule: "visual-diff", Severity: SeverityInfo,
			Message: fmt.Sprintf("%.2f%% of pixels changed (%d of %d, max %.2f%%); diff: %s/%s.diff.png", d.Pct, d.DiffPixels, d.TotalPixels, threshold, rel, slug)}
		if d.Pct > threshold {
			failedRoutes++
			f.Severity = SeverityError
			f.Suggestion = "if the change is intended, a human approves it on the PR; otherwise fix the regression"
		}
		gr.Findings = append(gr.Findings, f)
	}
	gr.Duration = timeNow().Sub(start)
	gr.ExitCode = 0
	gr.Summary = fmt.Sprintf("%d of %s over the %.2f%% pixel-diff threshold", failedRoutes, plural(len(routes), "route"), threshold)
	SortFindings(gr.Findings)
	if failedRoutes > 0 {
		gr.Status = StatusFail
	} else {
		gr.Status = StatusPass
	}
	return applyWarnOnly(gr, g)
}
