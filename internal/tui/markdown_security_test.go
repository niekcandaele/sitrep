package tui

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
)

func TestDirectDetailSourceBodiesCannotInjectTerminalOutput(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	rawC1OSC52 := string([]byte{0x9d}) + "52;c;cmF3LWMxLXBheWxvYWQ=" + string([]byte{0x9c})
	hostile := "café 東京 " + string([]byte{0xff, 0xc0, 0xaf}) +
		"\x1b]8;;https://evil.example.test/body\aEVIL-LABEL\x1b]8;;\x1b\\" +
		"\x1b]52;c;ZXNjLW9zYy01Mi1wYXlsb2Fk\a" + rawC1OSC52 +
		"\x1b[2J\r\n[safe link](https://safe.example.test/docs) #12 @alice"

	tests := []struct {
		name   string
		detail model.Detail
		caps   model.Capabilities
	}{
		{name: "description", detail: model.Detail{Description: hostile}},
		{name: "comment", detail: model.Detail{Comments: []model.Comment{{Body: hostile}}}, caps: model.Capabilities{Comments: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := renderDirectDetailSource(t, tt.detail, tt.caps, 100)
			if !utf8.ValidString(raw) {
				t.Fatalf("frame contains malformed UTF-8: % x", []byte(raw))
			}
			for _, forbidden := range []string{
				"https://evil.example.test/body", "ZXNjLW9zYy01Mi1wYXlsb2Fk", "cmF3LWMxLXBheWxvYWQ=",
				"\x1b]52;", "\x1b[2J", string(rune(0x9d)), string(rune(0x9c)),
			} {
				if strings.Contains(raw, forbidden) {
					t.Errorf("frame contains hostile terminal payload %q: %q", forbidden, raw)
				}
			}
			visible := ansi.Strip(raw)
			for _, want := range []string{"café", "東京", "safe link", "EVIL-LABEL"} {
				if !strings.Contains(visible, want) {
					t.Errorf("sanitized frame lost visible content %q:\n%s", want, visible)
				}
			}
			for _, target := range []string{
				"https://safe.example.test/docs",
				"https://github.com/acme/widgets/issues/12",
				"https://github.com/alice",
			} {
				if !strings.Contains(raw, target) {
					t.Errorf("frame lacks expected safe OSC 8 target %q: %q", target, raw)
				}
			}
			assertAllHyperlinksBalanced(t, raw)
		})
	}
}

func TestControlOnlyDirectDescriptionIsEmptyAfterSanitizing(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	in := DetailInput{Detail: model.Detail{Description: "\x1b]52;c;cHduZWQ=\a\x00\r"}}
	visible := ansi.Strip(strings.Join(detailLines(in, 80, Styles{}), "\n"))
	if !strings.Contains(visible, "No description.") {
		t.Errorf("control-only description did not render empty state:\n%s", visible)
	}
	if strings.Contains(visible, "cHduZWQ=") {
		t.Errorf("control-only description leaked OSC payload:\n%s", visible)
	}
}

func renderDirectDetailSource(t *testing.T, detail model.Detail, caps model.Capabilities, width int) string {
	t.Helper()
	open := OpenTicket{Ticket: model.Ticket{
		ID: "acme/widgets#40", Key: "#40", Title: "Markdown safety",
		URL: "https://github.com/acme/widgets/issues/40",
	}}
	m := New(t.Context(), Options{
		Open: &open,
		DetailSource: func(_ context.Context, _ model.TicketID) (model.Detail, model.Capabilities, error) {
			return detail, caps, nil
		},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	m = updated.(Model)
	message := m.detailFetchCmd(m.detailGeneration, m.detail.ticket.ID)()
	updated, _ = m.Update(message)
	m = updated.(Model)
	return m.View().Content
}
