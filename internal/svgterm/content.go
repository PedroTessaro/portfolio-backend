package svgterm

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/githubapi"
)

// buildLines is the session the terminal plays. Three commands, each followed
// by its own output, and every block degrades on its own if its data source is
// missing. Nothing here is written by hand except the identity, which comes
// from the config; the rest is whatever the API said a moment ago.
func buildLines(d Data, p Palette) []line {
	lines := []line{
		{typed: true, segs: []segment{
			{"$ ", p.Green},
			{"curl", p.Blue},
			{" -s ", p.Foreground},
			{"https://" + d.Cfg.Terminal.Host + "/whoami", p.Cyan},
		}},
	}
	lines = append(lines, whoamiLines(d, p)...)

	if len(d.Stats.Projects) > 0 {
		lines = append(lines, line{}, line{typed: true, segs: []segment{
			{"$ ", p.Green},
			{"ls", p.Blue},
			{" -lt ", p.Foreground},
			{"~/projects", p.Cyan},
		}})
		lines = append(lines, projectLines(d.Stats.Projects, p)...)
	}

	if !d.Stats.Last.Empty() {
		lines = append(lines, line{}, line{typed: true, segs: []segment{
			{"$ ", p.Green},
			{"git", p.Blue},
			{" log -1 ", p.Foreground},
			{"--oneline", p.Orange},
		}})
		lines = append(lines, commitLines(d.Stats.Last, p)...)
	}

	lines = append(lines, line{}, line{typed: true, segs: []segment{
		{"$ ", p.Green},
		{"./stats", p.Blue},
		{" --live", p.Orange},
	}})
	lines = append(lines, statsLines(d, p)...)

	return append(lines, line{}, line{segs: []segment{{"$ ", p.Green}}})
}

func whoamiLines(d Data, p Palette) []line {
	id := d.Cfg.Identity

	lines := []line{
		{segs: []segment{
			{"> ", p.Dim},
			{id.Name, p.Foreground},
			{" — ", p.Dim},
			{id.Role, p.Purple},
		}},
		{segs: stackLine(d.Cfg.Stack, p)},
	}

	if extra := joinNonEmpty(" · ", id.Location, id.Tagline); extra != "" {
		lines = append(lines, line{segs: []segment{{"> ", p.Dim}, {extra, p.Dim}}})
	}
	// The one line a recruiter is actually scanning for, so it gets a colour
	// rather than the dim treatment the rest of the metadata gets.
	if id.Availability != "" {
		lines = append(lines, line{segs: []segment{{"> ", p.Dim}, {id.Availability, p.Green}}})
	}
	return lines
}

// commitLines is the proof-of-life block: the most recent push, with its real
// message.
func commitLines(last githubapi.Commit, p Palette) []line {
	const maxMessage = 46

	message := last.Message
	if runeLen(message) > maxMessage {
		message = string([]rune(message)[:maxMessage-1]) + "…"
	}

	return []line{{segs: []segment{
		{"> ", p.Dim},
		{last.Repo + "  ", p.Cyan},
		{message, p.Foreground},
		{"  " + sinceRoughly(last.At), p.Dim},
	}}}
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

// projectLines lays the listing out in columns, padded to the widest entry so
// it reads like real ls output rather than a ragged list.
func projectLines(projects []githubapi.Project, p Palette) []line {
	nameWidth, langWidth := 0, 0
	for _, project := range projects {
		nameWidth = max(nameWidth, runeLen(project.Name))
		langWidth = max(langWidth, runeLen(project.Language))
	}

	lines := make([]line, 0, len(projects))
	for _, project := range projects {
		// A column of "★0" only advertises the stars that aren't there. The
		// space stays reserved so the columns still line up.
		stars := ""
		if project.Stars > 0 {
			stars = "★" + strconv.Itoa(project.Stars)
		}

		lines = append(lines, line{segs: []segment{
			{"> ", p.Dim},
			{padRight(project.Name, nameWidth) + "   ", p.Cyan},
			{padRight(project.Language, langWidth) + "   ", p.Dim},
			{padRight(stars, 4) + "  ", p.Yellow},
			{sinceRoughly(project.PushedAt), p.Dim},
		}})
	}
	return lines
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
	// Only worth saying once the numbers have some age on them; "cached 0s" is
	// noise on a request that just fetched them.
	switch age := d.Stats.Age(); {
	case d.Stats.Stale:
		githubLine.segs = append(githubLine.segs, segment{"   (stale)", p.Orange})
	case age >= time.Minute:
		githubLine.segs = append(githubLine.segs, segment{
			fmt.Sprintf("   (cached %s)", shortDur(age)), p.Dim,
		})
	}

	lines := []line{githubLine}

	// Written by the CI workflow, never by this service — so the numbers are
	// whatever the last run on main actually measured.
	if d.HasCI {
		status, statusColor := d.CI.Status, p.Green
		if status != "passing" {
			statusColor = p.Red
		}
		lines = append(lines, line{segs: []segment{
			{"> ", p.Dim},
			{"ci ", p.Dim},
			{status, statusColor},
			{"   tests ", p.Dim},
			{strconv.Itoa(d.CI.Tests), p.Yellow},
			{"   coverage ", p.Dim},
			{fmt.Sprintf("%.0f%%", d.CI.Coverage), p.Yellow},
		}})
	}

	// Percentiles over the rolling window of real requests, not a benchmark.
	if d.Latency.Samples > 0 {
		lines = append(lines, pairsLine([]pair{
			{"p50", shortLatency(d.Latency.P50), p.Purple},
			{"p95", shortLatency(d.Latency.P95), p.Purple},
			{"n", humanInt(d.Latency.Samples), p.Purple},
		}, p))
	}

	// There is no uptime to report on a platform that discards the process
	// between requests, so this says what is actually true of the invocation.
	instance := "cold"
	if !d.Cold {
		instance = "warm"
	}
	lines = append(lines, pairsLine([]pair{
		{"region", d.Region, p.Green},
		{"instance", instance, p.Green},
		{"served in", shortLatency(d.ServedIn), p.Green},
	}, p))

	// Without a configured store there is no counter to show.
	if d.HasViews {
		lines = append(lines, line{segs: []segment{
			{"> ", p.Dim},
			{"readme views ", p.Dim},
			{humanInt(d.Views.Total), p.Purple},
			{fmt.Sprintf("  (+%s today)", humanInt(d.Views.Today)), p.Dim},
		}})
	}
	return lines
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

func runeLen(s string) int { return len([]rune(s)) }

func padRight(s string, width int) string {
	if pad := width - runeLen(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func joinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, sep)
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

// sinceRoughly is the ls-style age of a push: one unit, no false precision.
func sinceRoughly(t time.Time) string {
	if t.IsZero() {
		return "-"
	}

	days := int(time.Since(t).Hours() / 24)
	switch {
	case days >= 365:
		return fmt.Sprintf("%dy ago", days/365)
	case days >= 30:
		return fmt.Sprintf("%dmo ago", days/30)
	case days >= 1:
		return fmt.Sprintf("%dd ago", days)
	}

	if hours := int(time.Since(t).Hours()); hours >= 1 {
		return fmt.Sprintf("%dh ago", hours)
	}
	return "just now"
}

// shortDur keeps the two largest useful units: "3d 4h", "12m".
func shortDur(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Minutes())/60, int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
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
