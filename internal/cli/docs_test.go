package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/config"
)

func TestDocumentationContract(t *testing.T) {
	readme := readProjectDoc(t, "README.md")
	contextDoc := readProjectDoc(t, "CONTEXT.md")

	watchlistIntro := strings.Index(readme, "**Watchlist**")
	epicMechanics := strings.Index(readme, "The four entry forms behave as follows:")
	if watchlistIntro < 0 || epicMechanics < 0 || watchlistIntro > epicMechanics {
		t.Error("README must introduce Watchlist and Selector before Epic mechanics")
	}

	for _, marker := range []string{
		"sitrep 111",
		"sitrep 38 39 40 41",
		"producer | sitrep -",
		"--query 'repo:acme/widgets is:issue label:agent'",
		"gh issue list",
		"glab issue list",
		"acli jira workitem search",
		"jira issue list",
		"max_tickets",
		"refresh_interval",
		"when no Profile is selected",
		"has at most one selected Profile",
		"Mouse capture is on by default",
		"Shift-drag",
		"`--no-mouse`",
		"`tab` and `shift+tab` to focus Links",
		"session-local **Trail**",
		"OSC 8 terminal hyperlinks",
		"Watchlist documents use schema version 2",
		"Decoded Ticket/Detail documents remain schema version 1",
	} {
		if !strings.Contains(readme, marker) {
			t.Errorf("README does not contain %q", marker)
		}
	}

	for _, marker := range []string{
		"**Watchlist**:", "**Selector**:", "**Ref**:", "**Query**:", "**Epic**:", "**Trail**:",
	} {
		if !strings.Contains(contextDoc, marker) {
			t.Errorf("CONTEXT.md does not define %q", marker)
		}
	}

	public := strings.ToLower(readme + "\n" + contextDoc)
	for _, obsolete := range []string{
		"delegated epic",
		"<ref> is the epic ref",
		"**epic ref**:",
	} {
		if strings.Contains(public, obsolete) {
			t.Errorf("public documentation retains obsolete framing %q", obsolete)
		}
	}
}

func TestREADMEStructuredExamplesUseRealContracts(t *testing.T) {
	readme := readProjectDoc(t, "README.md")

	yamlBlocks := fencedBlocks(readme, "yaml")
	if len(yamlBlocks) == 0 {
		t.Fatal("README has no YAML config example")
	}
	for i, block := range yamlBlocks {
		cfg, err := config.Parse(strings.NewReader(block), "README.md")
		if err != nil {
			t.Errorf("YAML block %d does not pass the strict config loader: %v", i+1, err)
			continue
		}
		if work, ok := cfg.Profiles["work-github"]; ok {
			if work.MaxTickets != 250 || work.RefreshInterval != 5*time.Minute {
				t.Errorf("work-github knobs = %d/%s, want 250/5m", work.MaxTickets, work.RefreshInterval)
			}
		}
	}

	jsonBlocks := fencedBlocks(readme, "json")
	if len(jsonBlocks) == 0 {
		t.Fatal("README has no JSON contract example")
	}
	for i, block := range jsonBlocks {
		if !json.Valid([]byte(block)) {
			t.Errorf("JSON block %d is not valid JSON", i+1)
		}
	}
}

func readProjectDoc(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(contents)
}

func fencedBlocks(document, language string) []string {
	opening := "```" + language + "\n"
	var blocks []string
	for {
		start := strings.Index(document, opening)
		if start < 0 {
			return blocks
		}
		document = document[start+len(opening):]
		end := strings.Index(document, "\n```")
		if end < 0 {
			return blocks
		}
		blocks = append(blocks, document[:end])
		document = document[end+4:]
	}
}
