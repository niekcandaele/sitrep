package tui

import (
	"reflect"
	"testing"

	help "charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

func namedDetailHelpKeyMap() DetailKeyMap {
	keys := DefaultDetailKeyMap()
	set := func(binding *key.Binding, name string) {
		binding.SetHelp(name, name+" action")
	}
	set(&keys.ToggleMouse, "mouse")
	set(&keys.MouseWheel, "mouse-wheel")
	set(&keys.MouseFollow, "mouse-follow")
	set(&keys.Back, "back")
	set(&keys.Quit, "quit")
	set(&keys.Follow, "follow")
	set(&keys.NextLink, "next-link")
	set(&keys.PreviousLink, "previous-link")
	set(&keys.Parent, "parent")
	set(&keys.Refresh, "refresh")
	set(&keys.Help, "help")
	set(&keys.Up, "up")
	set(&keys.Down, "down")
	set(&keys.PageUp, "page-up")
	set(&keys.PageDown, "page-down")
	set(&keys.Home, "home")
	set(&keys.End, "end")
	return keys
}

func detailHelpKeysOf(bindings []key.Binding) []string {
	keys := make([]string, len(bindings))
	for i, binding := range bindings {
		keys[i] = binding.Help().Key
	}
	return keys
}

func TestDetailHelpRolesMapEveryBindingByName(t *testing.T) {
	roles := detailHelpRolesFrom(namedDetailHelpKeyMap())
	got := map[string]string{
		"mouse":         roles.mouse.Help().Key,
		"mouse-wheel":   roles.mouseWheel.Help().Key,
		"mouse-follow":  roles.mouseFollow.Help().Key,
		"back":          roles.back.Help().Key,
		"quit":          roles.quit.Help().Key,
		"follow":        roles.follow.Help().Key,
		"next-link":     roles.nextLink.Help().Key,
		"previous-link": roles.previousLink.Help().Key,
		"parent":        roles.parent.Help().Key,
		"refresh":       roles.refresh.Help().Key,
		"help":          roles.help.Help().Key,
		"L":             roles.legend.Help().Key,
		"up":            roles.up.Help().Key,
		"down":          roles.down.Help().Key,
		"page-up":       roles.pageUp.Help().Key,
		"page-down":     roles.pageDown.Help().Key,
		"home":          roles.home.Help().Key,
		"end":           roles.end.Help().Key,
	}
	for name, keyName := range got {
		if keyName != name {
			t.Errorf("%s role uses %q", name, keyName)
		}
	}
	if got := roles.links.Help(); got.Key != "tab/⇧tab" || got.Desc != "links" {
		t.Errorf("combined Link role = %+v", got)
	}
	if roles.links.Enabled() != roles.nextLink.Enabled() {
		t.Error("combined Link role changed NextLink availability")
	}
}

func TestDetailHelpGroupsAreBuiltFromNamedRoles(t *testing.T) {
	keys := namedDetailHelpKeyMap()
	keys.Parent.SetEnabled(true)
	keys.NextLink.SetEnabled(true)
	keys.PreviousLink.SetEnabled(true)
	keys.Follow.SetEnabled(true)
	roles := detailHelpRolesFrom(keys)

	if got, want := detailHelpKeysOf(roles.defaultShortBindings()), []string{
		"mouse", "back", "quit", "follow", "tab/⇧tab",
		"up", "down", "parent", "refresh", "help",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("short roles = %v, want %v", got, want)
	}
	groups := roles.fullGroups()
	if got, want := detailHelpKeysOf(groups[0]), []string{
		"mouse", "mouse-wheel", "mouse-follow", "back", "follow", "next-link", "previous-link",
		"parent", "refresh", "help", "L", "quit",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("action roles = %v, want %v", got, want)
	}
	if got, want := detailHelpKeysOf(groups[1]), []string{
		"up", "down", "page-up", "page-down", "home", "end",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("scroll roles = %v, want %v", got, want)
	}
}

func TestCompactDetailShortHelpSelectsPriorityRolesByName(t *testing.T) {
	binding := func(name string) key.Binding {
		return key.NewBinding(
			key.WithKeys(name),
			key.WithHelp(name, name+" action"),
		)
	}
	roles := detailHelpRoles{
		refresh: binding("supplemental-refresh"),
		down:    binding("supplemental-down"),
		help:    binding("priority-help"),
		links:   binding("priority-links"),
		up:      binding("supplemental-up"),
		parent:  binding("priority-parent"),
		mouse:   binding("priority-mouse"),
		follow:  binding("priority-follow"),
		quit:    binding("priority-quit"),
		back:    binding("priority-back"),
	}
	renderer := help.New()
	renderer.SetWidth(0)
	priority := roles.priorityShortBindings()
	budget := lipgloss.Width(renderer.ShortHelpView(priority))

	got := compactDetailShortHelp(renderer, roles, budget)
	want := []string{
		"priority-mouse", "priority-back", "priority-quit",
		"priority-follow", "priority-links", "priority-parent",
		"priority-help",
	}
	if gotKeys := detailHelpKeysOf(got); !reflect.DeepEqual(gotKeys, want) {
		t.Errorf("compact priority roles = %v, want %v", gotKeys, want)
	}
}
