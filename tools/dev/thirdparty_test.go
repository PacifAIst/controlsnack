// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseReportReadsOneLinePerLibraryAndRefusesWhatItCannotName(t *testing.T) {
	good := "a.example/x\tv1.0.0\tMIT\t/m/x/LICENSE\thttps://a.example/x/LICENSE\n\nb.example/y\tv2.0.0\tApache-2.0\t/m/y/LICENSE.txt\thttps://b.example/y/LICENSE.txt\n"
	lines, err := parseReport(good)
	if err != nil {
		t.Fatal(err)
	}
	want := []libraryLine{
		{"a.example/x", "v1.0.0", "MIT", "/m/x/LICENSE", "https://a.example/x/LICENSE"},
		{"b.example/y", "v2.0.0", "Apache-2.0", "/m/y/LICENSE.txt", "https://b.example/y/LICENSE.txt"},
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("got %+v", lines)
	}
	for name, bad := range map[string]string{
		"too few fields":    "a.example/x\tv1\tMIT\n",
		"too many fields":   "a.example/x\tv1\tMIT\t/p\thttps://u\textra\n",
		"unknown license":   "a.example/x\tv1\tUnknown\t/p\thttps://u\n",
		"empty license":     "a.example/x\tv1\t\t/p\thttps://u\n",
		"no license file":   "a.example/x\tv1\tMIT\t\thttps://u\n",
		"unknown file path": "a.example/x\tv1\tMIT\tUnknown\thttps://u\n",
	} {
		if _, err := parseReport(bad); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if lines, err := parseReport(""); err != nil || len(lines) != 0 {
		t.Errorf("an empty report: %v %v", lines, err)
	}
}

func fakeFiles(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if text, ok := files[path]; ok {
			return []byte(text), nil
		}
		return nil, errors.New("no such file " + path)
	}
}

func TestMergeJoinsTheBinariesAndThePlatformsIntoOneEntryPerModule(t *testing.T) {
	read := fakeFiles(map[string]string{
		"/m/x/LICENSE": "MIT License\r\n\r\ntext of x\r\n",
		"/m/w/LICENSE": "old Mac\rline\r",
		"/m/y/LICENSE": "BSD text of y\n\n",
		"/m/z/LICENSE": "Apache text of z",
	})
	x := libraryLine{"a.example/x", "v1", "MIT", "/m/x/LICENSE", "https://u/x"}
	y := libraryLine{"a.example/y", "v1", "BSD-3-Clause", "/m/y/LICENSE", "https://u/a"}
	z := libraryLine{"a.example/z", "v1", "Apache-2.0", "/m/z/LICENSE", "https://u/z"}
	y2 := libraryLine{"a.example/y", "v1", "MIT", "/m/y/LICENSE", "https://u/b"} // a second license in one file
	w := libraryLine{"a.example/w", "v1", "MIT", "/m/w/LICENSE", "https://u/w"}
	libs, err := mergeLibraries([]source{
		{lines: []libraryLine{z, x, w}, host: "linux"},
		{lines: []libraryLine{x, y2, y}, host: "windows"},
		{lines: []libraryLine{x}, host: ""},
	}, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 4 || libs[0].Module != "a.example/w" || libs[1].Module != "a.example/x" || libs[2].Module != "a.example/y" || libs[3].Module != "a.example/z" {
		t.Fatalf("not sorted by module: %+v", libs)
	}
	wl, xl, yl, zl := libs[0], libs[1], libs[2], libs[3]
	if !xl.Agent || len(xl.Host) != 2 {
		t.Errorf("x is in both hosts and the agent: %+v", xl)
	}
	if yl.Agent || len(yl.Host) != 1 || !yl.Host["windows"] {
		t.Errorf("y is in the windows host only: %+v", yl)
	}
	if !reflect.DeepEqual(yl.Licenses, []string{"BSD-3-Clause", "MIT"}) || !reflect.DeepEqual(yl.URLs, []string{"https://u/a", "https://u/b"}) || len(yl.Texts) != 1 {
		t.Errorf("y: two licenses and two addresses sorted, one file, one text: %+v", yl)
	}
	if !reflect.DeepEqual(xl.Texts, []string{"MIT License\n\ntext of x"}) {
		t.Errorf("the text is trimmed and its line ends are normalized: %q", xl.Texts)
	}
	if !reflect.DeepEqual(wl.Texts, []string{"old Mac\nline"}) {
		t.Errorf("a lone carriage return is a line end: %q", wl.Texts)
	}
	if !reflect.DeepEqual(zl.URLs, []string{"https://u/z"}) {
		t.Errorf("z: %+v", zl)
	}
}

func TestMergeKeepsTwoVersionsOfOneModuleApartAndStopsAtAnUnreadableFile(t *testing.T) {
	read := fakeFiles(map[string]string{"/m/a": "text a", "/m/b": "text b", "/m/empty": "  \n"})
	libs, err := mergeLibraries([]source{{lines: []libraryLine{
		{"a.example/x", "v1", "MIT", "/m/a", "u"}, {"a.example/x", "v2", "MIT", "/m/b", "u"},
	}, host: "linux"}}, read)
	if err != nil || len(libs) != 2 || libs[0].Version != "v1" || libs[1].Version != "v2" {
		t.Fatalf("%+v %v", libs, err)
	}
	if _, err := mergeLibraries([]source{{lines: []libraryLine{{"a.example/x", "v1", "MIT", "/m/missing", "u"}}, host: "linux"}}, read); err == nil || !strings.Contains(err.Error(), "a.example/x") {
		t.Errorf("a missing file: %v", err)
	}
	if _, err := mergeLibraries([]source{{lines: []libraryLine{{"a.example/x", "v1", "MIT", "/m/empty", "u"}}, host: "linux"}}, read); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("an empty file: %v", err)
	}
}

func TestWhereALibraryIsBuiltInIsSaidInWords(t *testing.T) {
	all := []string{"darwin", "linux", "windows"}
	tests := []struct {
		lib  library
		want string
	}{
		{library{Host: map[string]bool{"darwin": true, "linux": true, "windows": true}}, "host"},
		{library{Host: map[string]bool{"windows": true}}, "host (windows)"},
		{library{Host: map[string]bool{"linux": true, "darwin": true}}, "host (darwin, linux)"},
		{library{Agent: true}, "agent"},
		{library{Host: map[string]bool{"linux": true, "darwin": true, "windows": true}, Agent: true}, "host, agent"},
		{library{Host: map[string]bool{"linux": true}, Agent: true}, "host (linux), agent"},
	}
	for _, tt := range tests {
		if got := tt.lib.builtInto(all); got != tt.want {
			t.Errorf("%+v: got %q, want %q", tt.lib, got, tt.want)
		}
	}
}

func TestAFenceIsLongerThanAnyRunOfBackTicksInTheText(t *testing.T) {
	for text, want := range map[string]string{
		"plain":           "```",
		"a `tick` here":   "```",
		"two `` ticks":    "```",
		"three ``` ticks": "````",
		"``````":          "```````",
		"``` and `````":   "``````",
		"":                "```",
	} {
		if got := fenceFor(text); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

func TestTheNoticeListsEveryLibraryWithItsTextInAFenceThatTheTextCannotClose(t *testing.T) {
	libs := []library{
		{Module: "a.example/x", Version: "v1", Licenses: []string{"MIT"}, URLs: []string{"https://u/x"}, Texts: []string{"MIT text"}, Host: map[string]bool{"linux": true}},
		{Module: "a.example/y", Version: "v2", Licenses: []string{"BSD-3-Clause", "MIT"}, URLs: []string{"https://u/y1", "https://u/y2"}, Texts: []string{"first", "has ``` inside"}, Agent: true},
	}
	doc := string(renderThirdParty(libs, []string{"linux"}))
	for _, want := range []string{
		"# Third-party licenses",
		"## Not distributed with the program",
		"## Embedded assets",
		"## Libraries\n\n| Module | Version | License | Built into |\n|---|---|---|---|\n",
		"\n## License texts\n",
		"| `a.example/x` | v1 | MIT | host |",
		"| `a.example/y` | v2 | BSD-3-Clause, MIT | agent |",
		"### a.example/x v1\n\nLicense: MIT. Source: https://u/x\n\n```text\nMIT text\n```\n",
		"### a.example/y v2\n\nLicense: BSD-3-Clause, MIT. Source: https://u/y1, https://u/y2\n\n```text\nfirst\n```\n\n````text\nhas ``` inside\n````\n",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the notice lacks %q", want)
		}
	}
	if strings.Index(doc, "a.example/x v1") > strings.Index(doc, "a.example/y v2") {
		t.Error("libraries are not in the order given")
	}
	if got := string(renderThirdParty(libs, []string{"linux"})); got != doc {
		t.Error("the notice is not deterministic")
	}
}

func TestTheHostSystemsAreTheDistinctPlatformsOfTheHostTargets(t *testing.T) {
	if got := hostSystems(); !reflect.DeepEqual(got, []string{"darwin", "linux", "windows"}) {
		t.Errorf("got %v", got)
	}
}

func TestTheNoticeOnDiskMustBeTheOneWantedUpToLineEnds(t *testing.T) {
	want := []byte("# Third-party licenses\n\ntext\n")
	if err := compareNotice(want, want); err != nil {
		t.Errorf("the same notice: %v", err)
	}
	if err := compareNotice([]byte("# Third-party licenses\r\n\r\ntext\r\n"), want); err != nil {
		t.Errorf("a checkout with CR LF: %v", err)
	}
	for name, have := range map[string]string{"changed": "# Third-party licenses\n\nother\n", "empty": "", "shorter": "# Third-party licenses\n"} {
		if err := compareNotice([]byte(have), want); err == nil || !strings.Contains(err.Error(), "out of date") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
