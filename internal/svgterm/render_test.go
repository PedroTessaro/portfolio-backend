package svgterm

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/config"
	"github.com/PedroTessaro/portfolio-backend/internal/githubapi"
	"github.com/PedroTessaro/portfolio-backend/internal/store"
)

func testData() Data {
	return Data{
		Cfg: &config.Config{
			Identity: config.Identity{
				Name:       "Pedro Tessaro",
				Role:       "Backend Engineer",
				Location:   "Brazil",
				GitHubUser: "PedroTessaro",
				Tagline:    "CS student",
			},
			Stack:    []string{"Go", "Java", "C/C++"},
			Terminal: config.Terminal{Host: "example.fly.dev", User: "tessaro", Machine: "portfolio"},
		},
		Stats: githubapi.Stats{
			Repos: 27, Stars: 9, Commits: 412, HasCommits: true, FetchedAt: time.Now(),
			Projects: []githubapi.Project{
				{Name: "portfolio-backend", Language: "Go", Stars: 3, PushedAt: time.Now().Add(-2 * time.Hour)},
				{Name: "RSSAggregator", Language: "Go", PushedAt: time.Now().Add(-200 * 24 * time.Hour)},
			},
		},
		Views:    store.Views{Total: 1234, Today: 37},
		HasViews: true,
		Region:   "gru",
		ServedIn: 8 * time.Millisecond,
	}
}

// Browsers drop a malformed SVG silently: the README would show a broken image
// with nothing in the server logs.
func TestRenderProducesWellFormedXML(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme Palette
		data  Data
	}{
		{"dark", ThemeByName("dark"), testData()},
		{"light", ThemeByName("light"), testData()},
		{"static", ThemeByName("dark"), func() Data { d := testData(); d.Static = true; return d }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoder := xml.NewDecoder(strings.NewReader(string(Render(tc.data, tc.theme))))
			for {
				_, err := decoder.Token()
				if err == io.EOF {
					return
				}
				if err != nil {
					t.Fatalf("malformed SVG: %v", err)
				}
			}
		})
	}
}

func TestRenderIncludesLiveData(t *testing.T) {
	out := string(Render(testData(), ThemeByName("dark")))

	for _, want := range []string{
		"Pedro Tessaro",
		"Backend Engineer",
		"example.fly.dev/whoami",
		"27",
		"412",
		"portfolio-backend",
		"RSSAggregator",
		"2h ago",
		"CS student",
		"1,234",
		"gru",
		"8ms",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SVG is missing %q", want)
		}
	}
}

func TestCommitsOmittedWithoutToken(t *testing.T) {
	d := testData()
	d.Stats.HasCommits = false
	d.Stats.Commits = 0

	if out := string(Render(d, ThemeByName("dark"))); strings.Contains(out, "commits") {
		t.Error("commits column should be dropped when there is no token")
	}
}

func TestStaticModeHasNoAnimation(t *testing.T) {
	d := testData()
	d.Static = true
	out := string(Render(d, ThemeByName("dark")))

	for _, forbidden := range []string{"<animate", "<set ", `clip-path="url(#type`} {
		if strings.Contains(out, forbidden) {
			t.Errorf("static mode should not emit %q", forbidden)
		}
	}
	if !strings.Contains(out, "Backend Engineer") {
		t.Error("static mode should show the content already revealed")
	}
}

func TestAnimatedModeAnimates(t *testing.T) {
	out := string(Render(testData(), ThemeByName("dark")))
	if !strings.Contains(out, "<animate") {
		t.Error("expected <animate> elements")
	}
	if !strings.Contains(out, `calcMode="discrete"`) {
		t.Error("typing depends on discrete calcMode to step per character")
	}
}

// The terminal text contains '>' and the config is user-supplied.
func TestSpecialCharactersAreEscaped(t *testing.T) {
	d := testData()
	d.Cfg.Identity.Name = `Pedro <script> & "quotes"`
	out := string(Render(d, ThemeByName("dark")))

	if strings.Contains(out, "<script>") {
		t.Error("markup leaked into the SVG unescaped")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("expected the name escaped in the document")
	}
}

func TestStepsCoversEveryCharacter(t *testing.T) {
	// n characters need n+1 states: the empty start plus one per character.
	if got, want := steps(3, 0, 9), "0;9;18;27"; got != want {
		t.Errorf("steps(3, 0, 9) = %q, want %q", got, want)
	}
}

func TestHumanInt(t *testing.T) {
	for in, want := range map[int]string{0: "0", 42: "42", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4321: "-4,321"} {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShortDur(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{3 * time.Hour, "3h 0m"},
		{50 * time.Hour, "2d 2h"},
	} {
		if got := shortDur(tc.in); got != tc.want {
			t.Errorf("shortDur(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The listing is the part most likely to drift, so it gets its own checks.
func TestProjectListing(t *testing.T) {
	out := string(Render(testData(), ThemeByName("dark")))

	if !strings.Contains(out, "ls") || !strings.Contains(out, "~/projects") {
		t.Error("expected the ls block")
	}
	// Padded to the longest name so the columns line up.
	if !strings.Contains(out, "portfolio-backend") || !strings.Contains(out, "RSSAggregator    ") {
		t.Error("names should be padded to a common width")
	}
	if !strings.Contains(out, "★3") {
		t.Error("a starred project should show its count")
	}
	if strings.Contains(out, "★0") {
		t.Error("zero stars should render as blank, not ★0")
	}
}

// Nothing to list is not an error; the block just disappears.
func TestProjectBlockOmittedWhenEmpty(t *testing.T) {
	d := testData()
	d.Stats.Projects = nil

	if out := string(Render(d, ThemeByName("dark"))); strings.Contains(out, "~/projects") {
		t.Error("the ls block should be dropped when there is nothing to list")
	}
}

func TestSinceRoughly(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		in   time.Time
		want string
	}{
		{now.Add(-30 * time.Minute), "just now"},
		{now.Add(-5 * time.Hour), "5h ago"},
		{now.Add(-3 * 24 * time.Hour), "3d ago"},
		{now.Add(-60 * 24 * time.Hour), "2mo ago"},
		{now.Add(-800 * 24 * time.Hour), "2y ago"},
		{time.Time{}, "-"},
	} {
		if got := sinceRoughly(tc.in); got != tc.want {
			t.Errorf("sinceRoughly(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
