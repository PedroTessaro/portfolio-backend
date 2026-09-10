// Package svgterm draws an animated terminal as a static SVG.
//
// GitHub serves README images through its Camo proxy, inside an <img> tag,
// where scripts don't run and external resources don't load. So the animation
// is SMIL and the font is whatever monospace the visitor already has.
//
// That last part is the catch: substitute fonts have different metrics, and a
// typing effect clipped in fixed 9px steps would land mid-character. Declaring
// textLength on every run of text pins the geometry to what we computed here.
package svgterm

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/config"
	"github.com/PedroTessaro/portfolio-backend/internal/githubapi"
	"github.com/PedroTessaro/portfolio-backend/internal/store"
)

const (
	fontSize   = 15.0
	charWidth  = 9.0
	lineHeight = 25.0
	padX       = 26.0
	titleBar   = 38.0
	padTop     = 24.0
	padBottom  = 22.0
	slackCols  = 3.0 // columns of slack past the longest line
	minWidth   = 640.0
)

// Animation timeline, in seconds.
const (
	startDelay   = 0.35
	perChar      = 0.045
	afterCommand = 0.28 // between hitting enter and the output showing up
	stagger      = 0.13
	blockGap     = 0.40
	blinkPeriod  = 1.06
)

const fontStack = "ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace"

type Data struct {
	Cfg      *config.Config
	Stats    githubapi.Stats
	Views    store.Views
	HasViews bool
	Latency  store.Latency
	CI       store.CIStatus
	HasCI    bool
	Region   string
	Cold     bool // this invocation started a fresh instance
	ServedIn time.Duration

	// Static draws the final frame with no SMIL, for renderers that ignore
	// animation and would otherwise show an empty window.
	Static bool
}

type segment struct {
	text  string
	color string
}

// A typed line is revealed character by character; the rest appear at once, the
// way real command output does.
type line struct {
	segs  []segment
	typed bool
}

func (l line) runeCount() int {
	n := 0
	for _, s := range l.segs {
		n += len([]rune(s.text))
	}
	return n
}

func (l line) plain() string {
	var b strings.Builder
	for _, s := range l.segs {
		b.WriteString(s.text)
	}
	return b.String()
}

func Render(d Data, p Palette) []byte {
	lines := buildLines(d, p)

	maxRunes := 0
	for _, l := range lines {
		if n := l.runeCount(); n > maxRunes {
			maxRunes = n
		}
	}

	width := (float64(maxRunes)+slackCols)*charWidth + 2*padX
	if width < minWidth {
		width = minWidth
	}
	height := titleBar + padTop + float64(len(lines))*lineHeight + padBottom

	var defs, body strings.Builder
	t := startDelay

	for i, l := range lines {
		if len(l.segs) == 0 {
			continue // blank line: takes up space, costs no time
		}
		y := titleBar + padTop + fontSize + float64(i)*lineHeight

		if d.Static {
			writeSegments(&body, l, y)
			continue
		}

		if l.typed {
			if i > 0 {
				t += blockGap
			}
			dur := float64(l.runeCount()) * perChar
			writeTypedLine(&defs, &body, l, i, y, t, dur, p)
			t += dur + afterCommand
			continue
		}

		writeOutputLine(&body, l, y, t)
		t += stagger
	}

	lastY := titleBar + padTop + fontSize + float64(len(lines)-1)*lineHeight
	promptRunes := float64(len([]rune(lines[len(lines)-1].segs[0].text)))
	cursorX := num(padX + promptRunes*charWidth)

	if d.Static {
		fmt.Fprintf(&body, `<rect x="%s" y="%s" width="%s" height="17" fill="%s" opacity="0.85"/>`,
			cursorX, num(lastY-13), num(charWidth), p.Foreground)
	} else {
		fmt.Fprintf(&body,
			`<rect x="%s" y="%s" width="%s" height="17" fill="%s" opacity="0">`+
				`<animate attributeName="opacity" values="1;0" keyTimes="0;0.5" calcMode="discrete" dur="%ss" begin="%ss" repeatCount="indefinite"/>`+
				`</rect>`,
			cursorX, num(lastY-13), num(charWidth), p.Foreground, num(blinkPeriod), num(t))
	}

	title := fmt.Sprintf("%s@%s: ~", d.Cfg.Terminal.User, d.Cfg.Terminal.Machine)
	return assemble(width, height, title, defs.String(), body.String(), lines, p)
}

func writeTypedLine(defs, body *strings.Builder, l line, idx int, y, begin, dur float64, p Palette) {
	n := l.runeCount()
	clipID := fmt.Sprintf("type%d", idx)

	// calcMode="discrete" is what separates typing from a curtain sliding open.
	fmt.Fprintf(defs,
		`<clipPath id="%s"><rect x="%s" y="%s" width="0" height="%s">`+
			`<animate attributeName="width" values="%s" calcMode="discrete" dur="%ss" begin="%ss" fill="freeze"/>`+
			`</rect></clipPath>`,
		clipID, num(padX), num(y-fontSize), num(lineHeight),
		steps(n, 0, charWidth), num(dur), num(begin))

	fmt.Fprintf(body, `<g clip-path="url(#%s)">`, clipID)
	writeSegments(body, l, y)
	body.WriteString(`</g>`)

	fmt.Fprintf(body,
		`<rect x="%s" y="%s" width="%s" height="17" fill="%s" opacity="0">`+
			`<set attributeName="opacity" to="0.85" begin="%ss"/>`+
			`<animate attributeName="x" values="%s" calcMode="discrete" dur="%ss" begin="%ss" fill="freeze"/>`+
			`<set attributeName="opacity" to="0" begin="%ss"/>`+
			`</rect>`,
		num(padX), num(y-13), num(charWidth), p.Green,
		num(begin), steps(n, padX, charWidth), num(dur), num(begin), num(begin+dur))
}

func writeOutputLine(body *strings.Builder, l line, y, begin float64) {
	fmt.Fprintf(body, `<g opacity="0"><set attributeName="opacity" to="1" begin="%ss" fill="freeze"/>`, num(begin))
	writeSegments(body, l, y)
	body.WriteString(`</g>`)
}

// Separate <text> per segment rather than <tspan> keeps the textLength maths
// trivial: every run starts at a known column.
func writeSegments(body *strings.Builder, l line, y float64) {
	offset := 0
	for _, s := range l.segs {
		rs := []rune(s.text)
		if len(rs) == 0 {
			continue
		}
		fmt.Fprintf(body,
			`<text x="%s" y="%s" fill="%s" textLength="%s" lengthAdjust="spacingAndGlyphs" xml:space="preserve">%s</text>`,
			num(padX+float64(offset)*charWidth), num(y), s.color,
			num(float64(len(rs))*charWidth), escape(s.text))
		offset += len(rs)
	}
}

func assemble(width, height float64, title, defs, body string, lines []line, p Palette) []byte {
	var b strings.Builder

	label := make([]string, 0, len(lines))
	for _, l := range lines {
		if txt := strings.TrimSpace(l.plain()); txt != "" {
			label = append(label, txt)
		}
	}

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprintf(&b,
		`<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s" role="img" aria-label="%s">`,
		num(width), num(height), num(width), num(height), escape(strings.Join(label, " · ")))
	fmt.Fprintf(&b, `<desc>%s</desc>`, escape(strings.Join(label, "\n")))

	fmt.Fprintf(&b, `<defs><clipPath id="window"><rect x="0" y="0" width="%s" height="%s" rx="10"/></clipPath>%s</defs>`,
		num(width), num(height), defs)

	fmt.Fprintf(&b, `<g clip-path="url(#window)">`)
	fmt.Fprintf(&b, `<rect width="%s" height="%s" fill="%s"/>`, num(width), num(height), p.Background)
	fmt.Fprintf(&b, `<rect width="%s" height="%s" fill="%s"/>`, num(width), num(titleBar), p.TitleBar)
	fmt.Fprintf(&b, `<rect y="%s" width="%s" height="1" fill="%s"/>`, num(titleBar), num(width), p.Border)
	b.WriteString(`</g>`)

	for i, c := range []string{p.Red, p.Yellow, p.Green} {
		fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="5.5" fill="%s"/>`, num(padX-4+float64(i)*20), num(titleBar/2), c)
	}

	fmt.Fprintf(&b,
		`<text x="%s" y="%s" fill="%s" font-family="%s" font-size="12" text-anchor="middle">%s</text>`,
		num(width/2), num(titleBar/2+4), p.Dim, fontStack, escape(title))

	fmt.Fprintf(&b, `<g font-family="%s" font-size="%s">%s</g>`, fontStack, num(fontSize), body)

	// Half-pixel offset keeps the border from rendering fuzzy.
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%s" height="%s" rx="10" fill="none" stroke="%s"/>`,
		num(width-1), num(height-1), p.Border)

	b.WriteString(`</svg>`)
	return []byte(b.String())
}

// steps builds the discrete animation values: one per character, plus the empty
// starting state.
func steps(count int, start, step float64) string {
	parts := make([]string, 0, count+1)
	for i := 0; i <= count; i++ {
		parts = append(parts, num(start+float64(i)*step))
	}
	return strings.Join(parts, ";")
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func escape(s string) string { return escaper.Replace(s) }
