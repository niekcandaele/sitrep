package plain

import "testing"

func TestPullRequestOverflow(t *testing.T) {
	tests := []struct {
		name  string
		shown int
		total int
		want  string
	}{
		{name: "nothing at all", shown: 0, total: 0, want: ""},
		{name: "one pull request, no total", shown: 1, total: 0, want: ""},
		{name: "one pull request, total agrees", shown: 1, total: 1, want: ""},
		{name: "no total counts what is held", shown: 3, total: 0, want: "+2 more"},
		{name: "total agrees with what is held", shown: 3, total: 3, want: "+2 more"},
		{name: "truncated connection reports the total", shown: 20, total: 34, want: "+33 more"},
		{name: "total below what is held cannot shrink the count", shown: 20, total: 5, want: "+19 more"},
		{name: "a total with nothing shown renders nothing", shown: 0, total: 34, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PullRequestOverflow(tc.shown, tc.total); got != tc.want {
				t.Errorf("PullRequestOverflow(%d, %d) = %q, want %q", tc.shown, tc.total, got, tc.want)
			}
		})
	}
}
