package heartbeat

import (
	"reflect"
	"testing"
)

func TestDeliverable(testInstance *testing.T) {
	cases := []struct {
		name        string
		destination DestinationRef
		supported   map[string]bool
		want        bool
	}{
		{"enabled and supported", DestinationRef{Type: "ntfy", Enabled: true}, map[string]bool{"ntfy": true}, true},
		{"disabled", DestinationRef{Type: "ntfy", Enabled: false}, map[string]bool{"ntfy": true}, false},
		{"enabled but unsupported", DestinationRef{Type: "discord", Enabled: true}, map[string]bool{"ntfy": true}, false},
		{"type mapped to false", DestinationRef{Type: "ntfy", Enabled: true}, map[string]bool{"ntfy": false}, false},
		{"nil set", DestinationRef{Type: "ntfy", Enabled: true}, nil, false},
		{"empty set", DestinationRef{Type: "ntfy", Enabled: true}, map[string]bool{}, false},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(subtest *testing.T) {
			if got := Deliverable(testCase.destination, testCase.supported); got != testCase.want {
				subtest.Fatalf("Deliverable = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestSupportedTypeList(testInstance *testing.T) {
	cases := []struct {
		name      string
		supported map[string]bool
		want      []string
	}{
		{"sorted true keys only", map[string]bool{"webhook": true, "ntfy": true, "discord": false}, []string{"ntfy", "webhook"}},
		{"nil", nil, []string{}},
		{"empty", map[string]bool{}, []string{}},
		{"all false", map[string]bool{"ntfy": false}, []string{}},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(subtest *testing.T) {
			got := supportedTypeList(testCase.supported)
			if got == nil || !reflect.DeepEqual(got, testCase.want) {
				subtest.Fatalf("supportedTypeList = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

func TestNewServiceCopiesSupportedTypes(testInstance *testing.T) {
	supported := map[string]bool{"ntfy": true}
	service := NewService(ServiceConfig{Store: &fakeStore{}, Messages: &fakeMessages{}, SupportedTypes: supported})
	destination := DestinationRef{Type: "ntfy", Enabled: true}
	if !service.Deliverable(destination) {
		testInstance.Fatal("expected deliverable before mutation")
	}
	supported["ntfy"] = false
	delete(supported, "ntfy")
	if !service.Deliverable(destination) {
		testInstance.Fatal("mutating the caller's map changed the service")
	}
}
