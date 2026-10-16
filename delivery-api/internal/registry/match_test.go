package registry

import (
	"reflect"
	"testing"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		{"star matches anything", "*", "anything", true},
		{"star matches empty", "*", "", true},
		{"exact equal", "backup_done", "backup_done", true},
		{"exact different", "backup_done", "backup_fail", false},
		{"exact is not prefix", "backup", "backup_done", false},
		{"prefix match", "backup_*", "backup_done", true},
		{"prefix matches bare prefix", "backup_*", "backup_", true},
		{"prefix is not substring", "backup_*", "x_backup_done", false},
		{"prefix shorter value", "backup_*", "backup", false},
		{"prefix different", "backup_*", "restore_done", false},
		{"single char prefix", "a*", "a", true},
		{"single char prefix mismatch", "a*", "b", false},
		{"prefix against empty value", "backup_*", "", false},
		{"double star is a prefix of a literal star", "**", "*x", true},
		{"double star does not match plain value", "**", "x", false},
		{"empty pattern empty value", "", "", true},
		{"empty pattern nonempty value", "", "x", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MatchPattern(test.pattern, test.value); got != test.want {
				t.Fatalf("MatchPattern(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
			}
		})
	}
}

func TestMatchRoutes(t *testing.T) {
	tests := []struct {
		name    string
		routes  []StoredRoute
		appID   string
		channel string
		want    []int64
	}{
		{
			name: "exact route",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "u4i", ChannelPattern: "backup_done", DestinationIDs: []int64{10}, Enabled: true},
			},
			appID: "u4i", channel: "backup_done",
			want: []int64{10},
		},
		{
			name: "overlapping routes union and dedupe ascending",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "u4i", ChannelPattern: "*", DestinationIDs: []int64{30, 10}, Enabled: true},
				{ID: 2, AppPattern: "*", ChannelPattern: "backup_*", DestinationIDs: []int64{20, 10}, Enabled: true},
				{ID: 3, AppPattern: "u4i", ChannelPattern: "backup_done", DestinationIDs: []int64{10, 5}, Enabled: true},
			},
			appID: "u4i", channel: "backup_done",
			want: []int64{5, 10, 20, 30},
		},
		{
			name: "disabled routes ignored",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{1}, Enabled: false},
				{ID: 2, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{2}, Enabled: true},
			},
			appID: "a", channel: "b",
			want: []int64{2},
		},
		{
			name: "star star matches everything",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{7}, Enabled: true},
			},
			appID: "anything", channel: "whatever",
			want: []int64{7},
		},
		{
			name: "duplicate id inside one route",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{1, 1}, Enabled: true},
			},
			appID: "a", channel: "b",
			want: []int64{1},
		},
		{
			name: "only a disabled route matches",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{1}, Enabled: false},
			},
			appID: "a", channel: "b",
			want: []int64{},
		},
		{
			name: "app mismatch",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "other", ChannelPattern: "*", DestinationIDs: []int64{1}, Enabled: true},
			},
			appID: "u4i", channel: "x",
			want: []int64{},
		},
		{
			name: "channel mismatch",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "backup_*", DestinationIDs: []int64{1}, Enabled: true},
			},
			appID: "u4i", channel: "restore_done",
			want: []int64{},
		},
		{
			name:   "nil routes",
			routes: nil,
			appID:  "u4i", channel: "x",
			want: []int64{},
		},
		{
			name: "route without destinations",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: nil, Enabled: true},
			},
			appID: "u4i", channel: "x",
			want: []int64{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MatchRoutes(test.routes, test.appID, test.channel)
			if got == nil {
				t.Fatal("MatchRoutes returned nil, want non-nil slice")
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("MatchRoutes = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMatchRoutesDoesNotMutateInput(t *testing.T) {
	routes := []StoredRoute{
		{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{3, 1, 2}, Enabled: true},
	}
	MatchRoutes(routes, "a", "b")
	if !reflect.DeepEqual(routes[0].DestinationIDs, []int64{3, 1, 2}) {
		t.Fatalf("input DestinationIDs mutated: %v", routes[0].DestinationIDs)
	}
}
