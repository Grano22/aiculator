// Command gifmake turns PNG frames into an animated GIF with nothing but the standard
// library, for the README's demo.
//
//	go run ./scripts/gifmake -out demo.gif -width 960 -delay 50 frame-*.png
//
// The dashboard is mostly flat colour, so one palette of the most used colours across all
// frames looks right without dithering. Each frame after the first keeps only the region
// that changed, with unchanged pixels transparent, which keeps the file small.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"slices"
)

func main() {
	out := flag.String("out", "demo.gif", "output file")
	width := flag.Int("width", 0, "scale frames down to this width; 0 keeps their size")
	delay := flag.Int("delay", 50, "time per frame, in hundredths of a second")
	last := flag.Int("hold", 200, "time the last frame stays, in hundredths of a second")
	flag.Parse()
	if flag.NArg() == 0 {
		fail(fmt.Errorf("no frames given"))
	}
	var frames []*image.RGBA
	for _, p := range flag.Args() {
		f, err := os.Open(p)
		if err != nil {
			fail(err)
		}
		img, err := png.Decode(f)
		_ = f.Close()
		if err != nil {
			fail(fmt.Errorf("%s: %w", p, err))
		}
		frames = append(frames, scale(img, *width))
	}
	pal := palette(frames)
	g := &gif.GIF{LoopCount: 0}
	near := map[uint32]uint8{}
	index := func(c color.RGBA) uint8 {
		k := uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
		if i, ok := near[k]; ok {
			return i
		}
		i := uint8(pal.Index(c))
		near[k] = i
		return i
	}
	const clear = 0 // Palette entry 0 is transparent.
	b := frames[0].Bounds()
	for n, fr := range frames {
		box := b
		if n > 0 {
			box = changed(frames[n-1], fr)
			if box.Empty() {
				box = image.Rect(0, 0, 1, 1)
			}
		}
		p := image.NewPaletted(box, pal)
		for y := box.Min.Y; y < box.Max.Y; y++ {
			for x := box.Min.X; x < box.Max.X; x++ {
				c := fr.RGBAAt(x, y)
				if n > 0 && c == frames[n-1].RGBAAt(x, y) {
					p.SetColorIndex(x, y, clear)
					continue
				}
				p.SetColorIndex(x, y, index(c))
			}
		}
		d := *delay
		if n == len(frames)-1 {
			d = *last
		}
		g.Image = append(g.Image, p)
		g.Delay = append(g.Delay, d)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	g.Config = image.Config{ColorModel: pal, Width: b.Dx(), Height: b.Dy()}
	f, err := os.Create(*out)
	if err != nil {
		fail(err)
	}
	if err := gif.EncodeAll(f, g); err != nil {
		fail(err)
	}
	if err := f.Close(); err != nil {
		fail(err)
	}
	st, _ := os.Stat(*out)
	fmt.Printf("%s: %d frames, %dx%d, %d KB\n", *out, len(frames), b.Dx(), b.Dy(), st.Size()/1024)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gifmake:", err)
	os.Exit(1)
}

// scale shrinks an image to a width by averaging the pixels each new pixel covers.
func scale(src image.Image, width int) *image.RGBA {
	sb := src.Bounds()
	if width <= 0 || width >= sb.Dx() {
		dst := image.NewRGBA(image.Rect(0, 0, sb.Dx(), sb.Dy()))
		for y := range sb.Dy() {
			for x := range sb.Dx() {
				dst.Set(x, y, src.At(sb.Min.X+x, sb.Min.Y+y))
			}
		}
		return dst
	}
	height := sb.Dy() * width / sb.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	fx, fy := float64(sb.Dx())/float64(width), float64(sb.Dy())/float64(height)
	for y := range height {
		y0, y1 := int(float64(y)*fy), max(int(float64(y+1)*fy), int(float64(y)*fy)+1)
		for x := range width {
			x0, x1 := int(float64(x)*fx), max(int(float64(x+1)*fx), int(float64(x)*fx)+1)
			var r, g, b, n uint32
			for sy := y0; sy < y1 && sy < sb.Dy(); sy++ {
				for sx := x0; sx < x1 && sx < sb.Dx(); sx++ {
					cr, cg, cb, _ := src.At(sb.Min.X+sx, sb.Min.Y+sy).RGBA()
					r, g, b, n = r+cr>>8, g+cg>>8, b+cb>>8, n+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), 255})
		}
	}
	return dst
}

// palette picks the 255 most used colours, after rounding each channel to 5 bits so that
// near-identical shades from anti-aliasing share an entry, plus a transparent entry.
func palette(frames []*image.RGBA) color.Palette {
	type bucket struct {
		n       int
		r, g, b int
	}
	counts := map[uint16]*bucket{}
	for _, f := range frames {
		for i := 0; i+3 < len(f.Pix); i += 4 {
			r, g, b := f.Pix[i], f.Pix[i+1], f.Pix[i+2]
			k := uint16(r>>3)<<10 | uint16(g>>3)<<5 | uint16(b>>3)
			c := counts[k]
			if c == nil {
				c = &bucket{}
				counts[k] = c
			}
			c.n++
			c.r += int(r)
			c.g += int(g)
			c.b += int(b)
		}
	}
	all := make([]*bucket, 0, len(counts))
	for _, c := range counts {
		all = append(all, c)
	}
	slices.SortFunc(all, func(a, b *bucket) int { return b.n - a.n })
	pal := color.Palette{color.RGBA{}}
	for _, c := range all {
		if len(pal) == 256 {
			break
		}
		pal = append(pal, color.RGBA{uint8(c.r / c.n), uint8(c.g / c.n), uint8(c.b / c.n), 255})
	}
	return pal
}

// changed returns the smallest rectangle holding every pixel that differs.
func changed(a, b *image.RGBA) image.Rectangle {
	r := image.Rectangle{}
	bb := b.Bounds()
	for y := bb.Min.Y; y < bb.Max.Y; y++ {
		row := y * b.Stride
		for x := bb.Min.X; x < bb.Max.X; x++ {
			i := row + x*4
			if a.Pix[i] != b.Pix[i] || a.Pix[i+1] != b.Pix[i+1] || a.Pix[i+2] != b.Pix[i+2] {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}
