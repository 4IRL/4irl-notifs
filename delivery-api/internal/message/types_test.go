package message_test

import (
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

func TestDisplayHasRenderableText(testInstance *testing.T) {
	cases := []struct {
		name    string
		display *message.Display
		want    bool
	}{
		{"nil display", nil, false},
		{"empty display", &message.Display{}, false},
		{"only a url and priority", &message.Display{URL: "https://example.com", Priority: "high"}, false},
		{"whitespace-only title", &message.Display{Title: " \t"}, false},
		{"whitespace-only body", &message.Display{Body: "\n\r\t "}, false},
		{"whitespace-only title and body with a url", &message.Display{Title: " ", Body: "\n", URL: "https://example.com"}, false},
		{"title", &message.Display{Title: "T"}, true},
		{"body", &message.Display{Body: "B"}, true},
		{"blank title and real body", &message.Display{Title: " ", Body: "B"}, true},
		{"real title and blank body", &message.Display{Title: "T", Body: "\n"}, true},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			if got := testCase.display.HasRenderableText(); got != testCase.want {
				testInstance.Errorf("HasRenderableText = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestDeriveStatus(testInstance *testing.T) {
	deliveriesIn := func(states ...string) []message.Delivery {
		deliveries := make([]message.Delivery, 0, len(states))
		for _, state := range states {
			deliveries = append(deliveries, message.Delivery{State: state})
		}
		return deliveries
	}

	cases := []struct {
		name       string
		deliveries []message.Delivery
		want       string
	}{
		{"nil list is pending", nil, message.StatusPending},
		{"empty list is pending", []message.Delivery{}, message.StatusPending},
		{"one pending", deliveriesIn(message.StatePending), message.StatusPending},
		{"one delivered", deliveriesIn(message.StateDelivered), message.StatusDelivered},
		{"one dead", deliveriesIn(message.StateDead), message.StatusDead},
		{"all delivered", deliveriesIn(message.StateDelivered, message.StateDelivered), message.StatusDelivered},
		{"all dead", deliveriesIn(message.StateDead, message.StateDead), message.StatusDead},
		{"delivered and dead is partial", deliveriesIn(message.StateDelivered, message.StateDead), message.StatusPartial},
		{"dead and delivered is partial", deliveriesIn(message.StateDead, message.StateDelivered), message.StatusPartial},
		{"pending beats delivered", deliveriesIn(message.StateDelivered, message.StatePending), message.StatusPending},
		{"pending beats dead", deliveriesIn(message.StateDead, message.StatePending), message.StatusPending},
		{"pending beats a partial mix", deliveriesIn(message.StateDelivered, message.StateDead, message.StatePending), message.StatusPending},
		{"an unknown state is not terminal", deliveriesIn(message.StateDelivered, "bogus"), message.StatusPending},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			if got := message.DeriveStatus(testCase.deliveries); got != testCase.want {
				testInstance.Errorf("DeriveStatus = %q, want %q", got, testCase.want)
			}
		})
	}
}
