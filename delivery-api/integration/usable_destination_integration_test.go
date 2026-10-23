//go:build integration

package integration

// Live-stack test of the usable-destination rule: an app-owned destination is
// never usable by another app, at write time (admin route create), at submit
// time (to.destination) and at delivery time (a route link that bypassed the
// write path through SQL). An app can now create an owned destination through
// the app-scoped API, but the test seeds one with SQL to stay independent of it.

import (
	"fmt"
	"net/http"
	"testing"
)

const (
	// usableOwnerApp owns the seeded destination; messageAppID ("itest") is the other app.
	usableOwnerApp = "itestown"
	// usableInsertDestinationSQL seeds a webhook destination the worker could never open or
	// dial; the test only ever asserts that submit refuses to use it.
	usableInsertDestinationSQL = `INSERT INTO destinations (name, type, owner_kind, owner_app_id, sealed_secrets)
VALUES ('%s', 'webhook', 'app', '%s', '\x01')`
	usableInsertBadRouteSQL = `WITH inserted AS (
	INSERT INTO routes (app_pattern, channel_pattern) VALUES ('*', '%s') RETURNING id
)
INSERT INTO route_destinations (route_id, destination_id)
SELECT inserted.id, destinations.id FROM inserted, destinations WHERE destinations.name = '%s'`
)

func TestAppOwnedDestinationIsUnusableByOtherApps(testInstance *testing.T) {
	waitForReady(testInstance)
	suffix := randomSuffix(testInstance)
	ownedName := usableOwnerApp + "_" + suffix
	absentName := "itest_absent_" + suffix
	channel := "it_" + suffix

	// Cleanups run last-in first: the route goes before the destination it links.
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("DELETE FROM destinations WHERE name = '%s'", ownedName))
	})
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("DELETE FROM routes WHERE channel_pattern = '%s'", channel))
	})
	psql(testInstance, fmt.Sprintf(usableInsertDestinationSQL, ownedName, usableOwnerApp))

	// Write time: a wildcard route cannot link the app-owned destination, and the
	// refusal is the one an absent destination gets.
	assertSameRefusal(testInstance, "POST route", func(destination string) (int, []byte) {
		return adminRequest(testInstance, http.MethodPost, "/routes", map[string]any{
			"app_pattern": "*", "channel_pattern": channel, "destinations": []string{destination},
		})
	}, ownedName, absentName, http.StatusBadRequest)

	// Submit time: app "itest" naming the owner's destination reads as an unknown name.
	assertSameRefusal(testInstance, "POST message to.destination", func(destination string) (int, []byte) {
		body := submitBody(channel, "it-usable-"+randomSuffix(testInstance))
		body["to"] = map[string]string{"destination": destination}
		return apiRequest(testInstance, http.MethodPost, messagesPath, body)
	}, ownedName, absentName, http.StatusBadRequest)

	// Delivery time: link the destination to a wildcard route behind the write path's
	// back. The message still matches no destination, so nothing is queued.
	psql(testInstance, fmt.Sprintf(usableInsertBadRouteSQL, channel, ownedName))
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath,
		submitBody(channel, "it-usable-routed-"+randomSuffix(testInstance)))
	requireStatus(testInstance, "POST message routed only to another app's destination", status,
		http.StatusUnprocessableEntity, body)
	requireErrorMessage(testInstance, "POST message routed only to another app's destination", body,
		"no destination matched")
}

// assertSameRefusal calls send with the other app's destination and with an
// absent one and requires the same status and the same body, so the first cannot
// be told apart from the second.
func assertSameRefusal(
	testInstance *testing.T, label string, send func(destination string) (int, []byte),
	ownedName, absentName string, wantStatus int,
) {
	testInstance.Helper()
	ownedStatus, ownedBody := send(ownedName)
	absentStatus, absentBody := send(absentName)
	requireStatus(testInstance, label+" naming another app's destination", ownedStatus, wantStatus, ownedBody)
	requireStatus(testInstance, label+" naming an absent destination", absentStatus, wantStatus, absentBody)
	if string(ownedBody) != string(absentBody) {
		testInstance.Fatalf("%s: another app's destination answered %s, an absent one %s; they must be identical",
			label, ownedBody, absentBody)
	}
}
