package jira

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// classify is the other documented business rule this driver exists for — an
// administrator may rename any link type — so it is tested directly as well as
// through the HTTP seam. The repository's naming convention for a test that
// needs the package's own scope is the _internal_test suffix.
func TestClassify(t *testing.T) {
	tests := []struct {
		name        string
		linkType    linkTypeWire
		wantInward  model.LinkKind
		wantOutward model.LinkKind
	}{
		{
			name:       "the stock blocking type",
			linkType:   linkTypeWire{ID: "10000", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
			wantInward: model.LinkBlockedBy, wantOutward: model.LinkBlocks,
		},
		{
			// The acceptance criterion: the wording is the signal, not the name.
			name:       "a renamed blocking type keeps its phrasing",
			linkType:   linkTypeWire{ID: "10000", Name: "Dependency", Inward: "is blocked by", Outward: "blocks"},
			wantInward: model.LinkBlockedBy, wantOutward: model.LinkBlocks,
		},
		{
			name:       "a type whose name alone says blocking",
			linkType:   linkTypeWire{ID: "10010", Name: "Blocks"},
			wantInward: model.LinkBlockedBy, wantOutward: model.LinkBlocks,
		},
		{
			name:       "blocking wording with the site's own capitalisation and spacing",
			linkType:   linkTypeWire{ID: "10011", Name: "Hard  Dependency", Inward: "IS  BLOCKED BY", Outward: "Blocks"},
			wantInward: model.LinkBlockedBy, wantOutward: model.LinkBlocks,
		},
		{
			name:       "relates",
			linkType:   linkTypeWire{ID: "10003", Name: "Relates", Inward: "relates to", Outward: "relates to"},
			wantInward: model.LinkRelates, wantOutward: model.LinkRelates,
		},
		{
			name:       "duplicate",
			linkType:   linkTypeWire{ID: "10002", Name: "Duplicate", Inward: "is duplicated by", Outward: "duplicates"},
			wantInward: model.LinkRelates, wantOutward: model.LinkRelates,
		},
		{
			name:       "cloners",
			linkType:   linkTypeWire{ID: "10001", Name: "Cloners", Inward: "is cloned by", Outward: "clones"},
			wantInward: model.LinkRelates, wantOutward: model.LinkRelates,
		},
		{
			name:       "an invented type falls back to Relates",
			linkType:   linkTypeWire{ID: "10007", Name: "Causes", Inward: "is caused by", Outward: "causes"},
			wantInward: model.LinkRelates, wantOutward: model.LinkRelates,
		},
		{
			name:       "a type with no wording at all",
			linkType:   linkTypeWire{ID: "10012", Name: "Mystery"},
			wantInward: model.LinkRelates, wantOutward: model.LinkRelates,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inward, outward := classify(tt.linkType)
			if inward != tt.wantInward || outward != tt.wantOutward {
				t.Errorf("classify(%+v) = (%v, %v), want (%v, %v)",
					tt.linkType, inward, outward, tt.wantInward, tt.wantOutward)
			}
		})
	}
}
