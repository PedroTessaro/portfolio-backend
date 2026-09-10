package svgterm

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// buildLines is the script the terminal plays: two commands, each followed by
// its output. Every block degrades on its own if its data source is missing.
func buildLines(d Data, p Palette) []line {
	id := d.Cfg.Identity

	lines := []line{
		{typed: true, segs: []segment{
			{"$ ", p.Green},
			{"curl", p.Blue},
			{" -s ", p.Foreground},
			{"https://" + d.Cfg.Terminal.Host + "/whoami", p.Cyan},
		}},
		{},
		{segs: []segment{
			{"> ", p.Dim},
			{id.Name, p.Foreground},
			{" — ", p.Dim},
			{id.Role, p.Purple},
		}},
		{segs: stackLine(d.Cfg.Stack, p)},
		{},
		{typed: true, segs: []segment{
			{"$ ", p.Green},
			{"./stats", p.Blue},
			{" --live", p.Orange},
		}},
		{},
	}

	lines = append(lines, statsLines(d, p)...)
	lines = append(lines, line{}, line{segs: []segment{{"$ ", p.Green}}})
	return lines
}

func stackLine(stack []string, p Palette) []segment {
	segs := []segment{{"> ", p.Dim}}
	for i, tech := range stack {
		if i > 0 {
			segs = append(segs, segment{" · ", p.Dim})
		}
		segs = append(segs, segment{tech, p.Cyan})
	}
	return segs
}

func statsLines(d Data, p Palette) []line {
	github := []pair{
		{"repos", strconv.Itoa(d.Stats.Repos), p.Yellow},
		{"stars", strconv.Itoa(d.Stats.Stars), p.Yellow},
	}
	if d.Stats.HasCommits {
		github = append(github, pair{"commits", humanInt(d.Stats.Commits), p.Yellow})
	}
	githubLine := pairsLine(github, p)
	if d.Stats.Stale {
		githubLine.segs = append(githubLine.segs, segment{"   (cached)", p.Dim})
	}

	runtime := pairsLine([]pair{
		{"region", d.Region, p.Green},
		{"up", shortDur(d.Uptime), p.Green},
		{"served in", shortLatency(d.ServedIn), p.Green},
	}, p)

	views := line{segs: []segment{
		{"> ", p.Dim},
		{"readme views ", p.Dim},
		{humanInt(d.Views.Total), p.Purple},
		{fmt.Sprintf("  (+%s today)", humanInt(d.Views.Today)), p.Dim},
	}}

	return []line{githubLine, runtime, views}
}

type pair struct {
	key, value, color string
}

func pairsLine(pairs []pair, p Palette) line {
	segs := []segment{{"> ", p.Dim}}
	for i, pr := range pairs {
		if i > 0 {
			segs = append(segs, segment{"   ", p.Dim})
		}
		segs = append(segs, segment{pr.key + " ", p.Dim}, segment{pr.value, pr.color})
	}
	return line{segs: segs}
}

func humanInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	var out strings.Builder
	for i, digit := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}
	if neg {
		return "-" + out.String()
	}
	return out.String()
}

// shortDur keeps the two largest useful units: "3d 4h", "12m 30s".
func shortDur(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Minutes())/60, int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(d.Seconds())/60, int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

func shortLatency(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	if ms := d.Milliseconds(); ms >= 1 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%dµs", d.Microseconds())
}
