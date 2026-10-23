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
		want    []DestinationMatch
	}{
		{
			name: "exact route",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "u4i", ChannelPattern: "backup_done", DestinationIDs: []int64{10}, Enabled: true},
			},
			appID: "u4i", channel: "backup_done",
			want: []DestinationMatch{{DestinationID: 10, RouteIDs: []int64{1}}},
		},
		{
			name: "overlapping routes union and dedupe ascending, keeping every matching route id",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "u4i", ChannelPattern: "*", DestinationIDs: []int64{30, 10}, Enabled: true},
				{ID: 2, AppPattern: "*", ChannelPattern: "backup_*", DestinationIDs: []int64{20, 10}, Enabled: true},
				{ID: 3, AppPattern: "u4i", ChannelPattern: "backup_done", DestinationIDs: []int64{10, 5}, Enabled: true},
			},
			appID: "u4i", channel: "backup_done",
			want: []DestinationMatch{
				{DestinationID: 5, RouteIDs: []int64{3}},
				{DestinationID: 10, RouteIDs: []int64{1, 2, 3}},
				{DestinationID: 20, RouteIDs: []int64{2}},
				{DestinationID: 30, RouteIDs: []int64{1}},
			},
		},
		{
			name: "disabled routes ignored",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{1}, Enabled: false},
				{ID: 2, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{2}, Enabled: true},
			},
			appID: "a", channel: "b",
			want: []DestinationMatch{{DestinationID: 2, RouteIDs: []int64{2}}},
		},
		{
			name: "star star matches everything",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{7}, Enabled: true},
			},
			appID: "anything", channel: "whatever",
			want: []DestinationMatch{{DestinationID: 7, RouteIDs: []int64{1}}},
		},
		{
			name: "duplicate id inside one route lists the route once",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{1, 1}, Enabled: true},
			},
			appID: "a", channel: "b",
			want: []DestinationMatch{{DestinationID: 1, RouteIDs: []int64{1}}},
		},
		{
			name: "only a disabled route matches",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: []int64{1}, Enabled: false},
			},
			appID: "a", channel: "b",
			want: []DestinationMatch{},
		},
		{
			name: "app mismatch",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "other", ChannelPattern: "*", DestinationIDs: []int64{1}, Enabled: true},
			},
			appID: "u4i", channel: "x",
			want: []DestinationMatch{},
		},
		{
			name: "channel mismatch",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "backup_*", DestinationIDs: []int64{1}, Enabled: true},
			},
			appID: "u4i", channel: "restore_done",
			want: []DestinationMatch{},
		},
		{
			name:   "nil routes",
			routes: nil,
			appID:  "u4i", channel: "x",
			want: []DestinationMatch{},
		},
		{
			name: "route without destinations",
			routes: []StoredRoute{
				{ID: 1, AppPattern: "*", ChannelPattern: "*", DestinationIDs: nil, Enabled: true},
			},
			appID: "u4i", channel: "x",
			want: []DestinationMatch{},
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

func TestDestinationUsableBy(t *testing.T) {
	owner := "urls4irl"
	emptyOwner := ""
	tests := []struct {
		name       string
		ownerKind  string
		ownerAppID *string
		appID      string
		want       bool
	}{
		{"operator owned is usable by any app", OwnerOperator, nil, "urls4irl", true},
		{"operator owned is usable by another app", OwnerOperator, nil, "chores4irl", true},
		{"operator owned is usable by the wildcard pattern", OwnerOperator, nil, "*", true},
		{"app owned is usable by its owner", OwnerApp, &owner, "urls4irl", true},
		{"app owned is not usable by another app", OwnerApp, &owner, "chores4irl", false},
		{"app owned is not usable by a prefix of the owner", OwnerApp, &owner, "urls4", false},
		{"app owned is not usable by a wildcard pattern", OwnerApp, &owner, "*", false},
		{"app owned is not usable by a prefix wildcard pattern", OwnerApp, &owner, "urls4irl*", false},
		{"app owned is not usable by an empty app id", OwnerApp, &owner, "", false},
		{"app owned without an owner id is never usable", OwnerApp, nil, "urls4irl", false},
		{"app owned with an empty owner id is never usable", OwnerApp, &emptyOwner, "", false},
		{"operator owned carrying an owner id fails closed", OwnerOperator, &owner, "urls4irl", false},
		{"operator owned carrying an owner id fails closed for another app", OwnerOperator, &owner, "chores4irl", false},
		{"empty owner kind fails closed", "", nil, "urls4irl", false},
		{"unknown owner kind fails closed", "person", nil, "urls4irl", false},
		{"unknown owner kind with an owner id fails closed", "person", &owner, "urls4irl", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DestinationUsableBy(test.ownerKind, test.ownerAppID, test.appID); got != test.want {
				t.Fatalf("DestinationUsableBy(%q, %v, %q) = %v, want %v",
					test.ownerKind, test.ownerAppID, test.appID, got, test.want)
			}
			destination := StoredDestination{OwnerKind: test.ownerKind, OwnerAppID: test.ownerAppID}
			if got := destination.UsableBy(test.appID); got != test.want {
				t.Fatalf("StoredDestination.UsableBy(%q) = %v, want %v", test.appID, got, test.want)
			}
		})
	}
}
