package tui

import (
	"reflect"
	"testing"

	"charm.land/bubbles/v2/key"
)

func namedDetailHelpKeyMap() DetailKeyMap {
	keys := DefaultDetailKeyMap()
	set := func(binding *key.Binding, name string) {
		binding.SetHelp(name, name+" action")
	}
	set(&keys.ToggleMouse, "mouse")
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
		"back":          roles.back.Help().Key,
		"quit":          roles.quit.Help().Key,
		"follow":        roles.follow.Help().Key,
		"next-link":     roles.nextLink.Help().Key,
		"previous-link": roles.previousLink.Help().Key,
		"parent":        roles.parent.Help().Key,
		"refresh":       roles.refresh.Help().Key,
		"help":          roles.help.Help().Key,
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
		"mouse", "back", "follow", "next-link", "previous-link",
		"parent", "refresh", "help", "quit",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("action roles = %v, want %v", got, want)
	}
	if got, want := detailHelpKeysOf(groups[1]), []string{
		"up", "down", "page-up", "page-down", "home", "end",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("scroll roles = %v, want %v", got, want)
	}
}
