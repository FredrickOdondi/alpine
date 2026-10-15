package gates

import (
	"image"
	"image/color"
)

// DiffResult is the outcome of comparing two screenshots.
type DiffResult struct {
	Width, Height int
	DiffPixels    int
	TotalPixels   int
	// Pct is DiffPixels / TotalPixels * 100.
	Pct float64
	// Image highlights differing pixels in red over a faded copy of "after".
	Image *image.NRGBA
}

// PixelDiff compares before and after pixel by pixel. Two pixels are equal
// when every RGBA channel differs by at most tolerance (0..1, as a fraction of
// 255), which absorbs anti-aliasing noise. Images of different sizes are
// compared on the union of both bounds; pixels present in only one image
// count as different. The result is deterministic.
func PixelDiff(before, after image.Image, tolerance float64) DiffResult {
	bb, ab := before.Bounds(), after.Bounds()
	w, h := max(bb.Dx(), ab.Dx()), max(bb.Dy(), ab.Dy())
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	limit := int(tolerance*255 + 0.5)
	res := DiffResult{Width: w, Height: h, TotalPixels: w * h, Image: out}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			bp, bok := pixelAt(before, bb, x, y)
			ap, aok := pixelAt(after, ab, x, y)
			if bok && aok && channelDistance(bp, ap) <= limit {
				// Faded grayscale of the unchanged pixel.
				g := (299*int(ap.R) + 587*int(ap.G) + 114*int(ap.B)) / 1000
				f := uint8(255 - (255-g)/4)
				out.SetNRGBA(x, y, color.NRGBA{f, f, f, 255})
				continue
			}
			res.DiffPixels++
			out.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 255})
		}
	}
	if res.TotalPixels > 0 {
		res.Pct = float64(res.DiffPixels) * 100 / float64(res.TotalPixels)
	}
	return res
}

func pixelAt(img image.Image, b image.Rectangle, x, y int) (color.NRGBA, bool) {
	if x >= b.Dx() || y >= b.Dy() {
		return color.NRGBA{}, false
	}
	return color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA), true
}

func channelDistance(a, b color.NRGBA) int {
	d := absDiff(a.R, b.R)
	d = max(d, absDiff(a.G, b.G))
	d = max(d, absDiff(a.B, b.B))
	return max(d, absDiff(a.A, b.A))
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
