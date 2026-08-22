package provider

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

type unknownSelector struct{}

func (unknownSelector) selector() {}

func TestCheckSelectorSupportAcceptsDeclaredSelectors(t *testing.T) {
	caps := model.Capabilities{Selectors: model.SelectorCapabilities{
		Epic:    true,
		RefList: true,
		Query:   true,
	}}

	for _, selector := range []Selector{EpicSelector{}, RefListSelector{}, QuerySelector{}} {
		if err := CheckSelectorSupport("test", caps, selector); err != nil {
			t.Errorf("CheckSelectorSupport(%T) error = %v", selector, err)
		}
	}
}

func TestCheckSelectorSupportRejectsUndeclaredSelectors(t *testing.T) {
	tests := []struct {
		selector Selector
		want     string
	}{
		{selector: EpicSelector{}, want: "test: Epic Selector is not supported"},
		{selector: RefListSelector{}, want: "test: Ref-list Selector is not supported"},
		{selector: QuerySelector{}, want: "test: Query Selector is not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			err := CheckSelectorSupport("test", model.Capabilities{}, tt.selector)
			if err == nil {
				t.Fatal("CheckSelectorSupport() error = nil")
			}
			if err.Error() != tt.want {
				t.Errorf("error = %q, want %q", err, tt.want)
			}
			if got := KindOf(err); got != KindBadRef {
				t.Errorf("KindOf(error) = %s, want %s", got, KindBadRef)
			}
			if KindOf(err).Retryable() {
				t.Error("unsupported Selector error is retryable")
			}
		})
	}
}

func TestCheckSelectorSupportRejectsUnknownAndPointerSelectors(t *testing.T) {
	caps := model.Capabilities{Selectors: model.SelectorCapabilities{
		Epic:    true,
		RefList: true,
		Query:   true,
	}}

	for _, selector := range []Selector{nil, unknownSelector{}, &EpicSelector{}, &RefListSelector{}, &QuerySelector{}} {
		err := CheckSelectorSupport("test", caps, selector)
		if err == nil {
			t.Fatalf("CheckSelectorSupport(%T) error = nil", selector)
		}
		if got := KindOf(err); got != KindBadRef {
			t.Errorf("CheckSelectorSupport(%T) Kind = %s, want %s", selector, got, KindBadRef)
		}
	}
}

func TestSelectorCapabilitiesRemainComparable(t *testing.T) {
	want := model.SelectorCapabilities{Epic: true, RefList: true, Query: true}
	got := want
	if got != want {
		t.Fatalf("SelectorCapabilities = %+v, want %+v", got, want)
	}
}
