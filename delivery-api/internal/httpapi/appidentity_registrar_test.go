package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
)

const (
	sampleRegistrarJSON = `{"id":2,"prefix":"hosted_","github_repository":"GPropersi/apps","github_repository_id":55,` +
		`"github_owner_id":7,"workflow_path":".github/workflows/deploy.yml","git_ref":"refs/heads/main","environment":null,` +
		`"approved_at":"2026-10-22T12:00:00Z","app_count":%d,"app_limit":50}`
	registrarBindingBase = `{"app_id":"hosted_a","github_repository":"GPropersi/apps","github_repository_id":55,` +
		`"github_owner_id":7,"workflow_path":".github/workflows/deploy.yml","git_ref":"refs/heads/main",` +
		`"environment":null,"approved_at":"2026-10-22T12:00:00Z",`
)

func sampleWorkflowRegistrar(appCount int) appidentity.WorkflowRegistrar {
	return appidentity.WorkflowRegistrar{
		ID: 2, Prefix: "hosted_", Repository: "GPropersi/apps", RepositoryID: 55, RepositoryOwnerID: 7,
		WorkflowPath: ".github/workflows/deploy.yml", Ref: "refs/heads/main", ApprovedAt: identityTime(),
		AppCount: appCount, AppLimit: appidentity.RegistrarAppLimit,
	}
}

func sampleRegistrarAppBinding() appidentity.Binding {
	registrarID := int64(2)
	binding := sampleIdentityBinding()
	binding.AppID, binding.Repository, binding.RepositoryID, binding.RepositoryOwnerID = "hosted_a", "GPropersi/apps", 55, 7
	binding.WorkflowPath = ".github/workflows/deploy.yml"
	binding.RegistrarID, binding.RegistrarPrefix = &registrarID, "hosted_"
	return binding
}

func TestExchangeBodyNameAndAppIDReachTheService(testInstance *testing.T) {
	testCases := []struct {
		name     string
		body     string
		wantApp  string
		wantName string
	}{
		{"app id only", `{"app_id":"urls4irl","token":"t"}`, "urls4irl", ""},
		{"name only", `{"name":"blog","token":"t"}`, "", "blog"},
		{"both are passed through for the service to refuse", `{"app_id":"a","name":"b","token":"t"}`, "a", "b"},
		{"neither is passed through for the service to refuse", `{"token":"t"}`, "", ""},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppIdentity{exchangeResult: appidentity.ExchangeResult{Status: appidentity.ExchangePending, RequestID: 1}}

			recorder := serveIdentity(fake, nil, http.MethodPost, exchangePath, testCase.body)

			assertResponse(subTest, recorder, http.StatusAccepted, `{"status":"pending","request_id":1}`+"\n")
			if fake.exchangedApp != testCase.wantApp || fake.exchangedName != testCase.wantName {
				subTest.Errorf("service saw app %q name %q, want %q %q", fake.exchangedApp, fake.exchangedName, testCase.wantApp, testCase.wantName)
			}
		})
	}
}

func TestExchangeNeitherOrBothMapsToTheTargetError(testInstance *testing.T) {
	recorder := serveIdentity(&fakeAppIdentity{err: appidentity.ErrExchangeTarget}, nil, http.MethodPost, exchangePath,
		`{"app_id":"a","name":"b","token":"t"}`)

	assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"send exactly one of app_id or name"}`+"\n")
}

func TestExchangeNameIssuedBodyCarriesTheDerivedAppID(testInstance *testing.T) {
	fake := &fakeAppIdentity{exchangeResult: appidentity.ExchangeResult{
		Status: appidentity.ExchangeIssued, AppID: "hosted_blog", Key: identityTestKey, KeyID: 8,
	}}

	recorder := serveIdentity(fake, nil, http.MethodPost, exchangePath, `{"name":"blog","token":"t"}`)

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"status":"issued","app_id":"hosted_blog","key":"`+identityTestKey+`","key_id":8}`+"\n")
}

func TestApproveRegistrarRoute(testInstance *testing.T) {
	fake := &fakeAppIdentity{registrar: sampleWorkflowRegistrar(0)}

	recorder := serveIdentity(fake, nil, http.MethodPost, identityBase+"/requests/7/approve-registrar", `{"prefix":"hosted_"}`)

	assertResponse(testInstance, recorder, http.StatusCreated, `{"registrar":`+strings.Replace(sampleRegistrarJSON, "%d", "0", 1)+`}`+"\n")
	if fake.approvedID != 7 || fake.approvedPrefix != "hosted_" {
		testInstance.Errorf("service saw request %d prefix %q", fake.approvedID, fake.approvedPrefix)
	}
}

func TestApproveRegistrarRejectsBadBodiesBeforeTheService(testInstance *testing.T) {
	for name, body := range map[string]string{
		"empty":         ``,
		"not json":      `nope`,
		"unknown field": `{"prefix":"hosted_","extra":1}`,
		"wrong type":    `{"prefix":1}`,
	} {
		testInstance.Run(name, func(subTest *testing.T) {
			fake := &fakeAppIdentity{}

			recorder := serveIdentity(fake, nil, http.MethodPost, identityBase+"/requests/7/approve-registrar", body)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
			if fake.calls != 0 {
				subTest.Errorf("service called %d times", fake.calls)
			}
		})
	}
}

func TestApproveRegistrarRejectsABadRequestID(testInstance *testing.T) {
	fake := &fakeAppIdentity{}

	recorder := serveIdentity(fake, nil, http.MethodPost, identityBase+"/requests/abc/approve-registrar", `{"prefix":"hosted_"}`)

	assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"invalid request id"}`+"\n")
	if fake.calls != 0 {
		testInstance.Errorf("service called %d times", fake.calls)
	}
}

func TestListRegistrarsCarriesAppCountAndLimit(testInstance *testing.T) {
	fake := &fakeAppIdentity{registrars: []appidentity.WorkflowRegistrar{sampleWorkflowRegistrar(3)}}

	recorder := serveIdentity(fake, nil, http.MethodGet, identityBase+"/registrars", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"registrars":[`+strings.Replace(sampleRegistrarJSON, "%d", "3", 1)+`]}`+"\n")
}

func TestListRegistrarsIsNeverNull(testInstance *testing.T) {
	recorder := serveIdentity(&fakeAppIdentity{}, nil, http.MethodGet, identityBase+"/registrars", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"registrars":[]}`+"\n")
}

func TestListRegistrarsEnvironmentIsSerialisedWhenSet(testInstance *testing.T) {
	registrar := sampleWorkflowRegistrar(0)
	registrar.Environment = "production"

	recorder := serveIdentity(&fakeAppIdentity{registrars: []appidentity.WorkflowRegistrar{registrar}}, nil,
		http.MethodGet, identityBase+"/registrars", "")

	if !strings.Contains(recorder.Body.String(), `"environment":"production"`) {
		testInstance.Errorf("body = %s, want the environment", recorder.Body.String())
	}
}

func TestDeleteRegistrar(testInstance *testing.T) {
	fake := &fakeAppIdentity{deleted: true, unboundCount: 4}

	recorder := serveIdentity(fake, nil, http.MethodDelete, identityBase+"/registrars/2", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"id":2,"deleted":true,"unbound_apps":4}`+"\n")
	if fake.deletedRegistrar != 2 {
		testInstance.Errorf("service saw registrar %d", fake.deletedRegistrar)
	}
}

func TestDeleteRegistrarOfAnUnknownIDIsIdempotent(testInstance *testing.T) {
	recorder := serveIdentity(&fakeAppIdentity{}, nil, http.MethodDelete, identityBase+"/registrars/9", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"id":9,"deleted":false,"unbound_apps":0}`+"\n")
}

func TestDeleteRegistrarRejectsAMalformedID(testInstance *testing.T) {
	for _, rawID := range []string{"abc", "0", "-1", "01"} {
		testInstance.Run(rawID, func(subTest *testing.T) {
			fake := &fakeAppIdentity{}

			recorder := serveIdentity(fake, nil, http.MethodDelete, identityBase+"/registrars/"+rawID, "")

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid registrar id"}`+"\n")
			if fake.calls != 0 {
				subTest.Errorf("service called %d times", fake.calls)
			}
		})
	}
}

func TestRequestsListCarriesKindAndNullAppIDForARegistrarRequest(testInstance *testing.T) {
	request := sampleIdentityRequest(appidentity.RequestPending)
	request.Kind, request.AppID = appidentity.RequestKindRegistrar, ""

	recorder := serveIdentity(&fakeAppIdentity{requests: []appidentity.Request{request}}, nil,
		http.MethodGet, identityBase+"/requests", "")

	body := recorder.Body.String()
	if !strings.Contains(body, `"kind":"registrar","app_id":null`) {
		testInstance.Errorf("body = %s, want kind registrar and app_id null", body)
	}
}

func TestBindingBodyCarriesRegistrarAndAcknowledgedAt(testInstance *testing.T) {
	acknowledgedAt := identityTime()
	binding := sampleRegistrarAppBinding()
	binding.AcknowledgedAt = &acknowledgedAt
	fake := &fakeAppIdentity{bindings: []appidentity.BindingWithKeys{{Binding: binding}}}

	recorder := serveIdentity(fake, nil, http.MethodGet, identityBase+"/bindings", "")

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"bindings":[`+registrarBindingBase+`"registrar":{"id":2,"prefix":"hosted_"},`+
			`"acknowledged_at":"2026-10-22T12:00:00Z","keys":[]}]}`+"\n")
}

func TestBindingBodyOfAnUnseenRegistrarAppHasNullAcknowledgedAt(testInstance *testing.T) {
	fake := &fakeAppIdentity{bindings: []appidentity.BindingWithKeys{{Binding: sampleRegistrarAppBinding()}}}

	recorder := serveIdentity(fake, nil, http.MethodGet, identityBase+"/bindings", "")

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"bindings":[`+registrarBindingBase+`"registrar":{"id":2,"prefix":"hosted_"},`+
			`"acknowledged_at":null,"keys":[]}]}`+"\n")
}

func TestAcknowledgeBinding(testInstance *testing.T) {
	fake := &fakeAppIdentity{acknowledged: appidentity.BindingWithKeys{Binding: sampleRegistrarAppBinding()}}

	recorder := serveIdentity(fake, nil, http.MethodPost, identityBase+"/bindings/hosted_a/acknowledge", "")

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"binding":`+registrarBindingBase+`"registrar":{"id":2,"prefix":"hosted_"},"acknowledged_at":null,"keys":[]}}`+"\n")
	if fake.acknowledgedApp != "hosted_a" {
		testInstance.Errorf("service saw app %q", fake.acknowledgedApp)
	}
}

func TestAcknowledgeAllBindings(testInstance *testing.T) {
	fake := &fakeAppIdentity{acknowledgedN: 2}

	recorder := serveIdentity(fake, nil, http.MethodPost, identityBase+"/bindings/acknowledge-all", `{"app_ids":["hosted_a","hosted_b"]}`)

	assertResponse(testInstance, recorder, http.StatusOK, `{"acknowledged":2}`+"\n")
	if len(fake.acknowledgedApps) != 2 || fake.acknowledgedApps[0] != "hosted_a" || fake.acknowledgedApps[1] != "hosted_b" {
		testInstance.Errorf("service saw %v, want the listed ids", fake.acknowledgedApps)
	}
}

func TestAcknowledgeAllRepeatAnswersZero(testInstance *testing.T) {
	recorder := serveIdentity(&fakeAppIdentity{acknowledgedN: 0}, nil, http.MethodPost,
		identityBase+"/bindings/acknowledge-all", `{"app_ids":["hosted_a"]}`)

	assertResponse(testInstance, recorder, http.StatusOK, `{"acknowledged":0}`+"\n")
}

func TestAcknowledgeAllRejectsBadBodiesBeforeTheService(testInstance *testing.T) {
	for name, body := range map[string]string{
		"empty body":    ``,
		"unknown field": `{"app_ids":["a"],"extra":1}`,
		"wrong type":    `{"app_ids":"a"}`,
	} {
		testInstance.Run(name, func(subTest *testing.T) {
			fake := &fakeAppIdentity{}

			recorder := serveIdentity(fake, nil, http.MethodPost, identityBase+"/bindings/acknowledge-all", body)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
			if fake.calls != 0 {
				subTest.Errorf("service called %d times", fake.calls)
			}
		})
	}
}

// The count and id validation live in the real service; this runs the real service's checks
// through the HTTP mapping (it fails before touching the store).
func TestAcknowledgeAllValidationErrorsReachTheClient(testInstance *testing.T) {
	tooMany := `["` + strings.Repeat(`a","`, appidentity.MaxAcknowledgeApps) + `a"]`
	testCases := []struct {
		name string
		body string
		want string
	}{
		{"missing", `{}`, "app_ids must list 1 to 200 apps"},
		{"empty", `{"app_ids":[]}`, "app_ids must list 1 to 200 apps"},
		{"over 200", `{"app_ids":` + tooMany + `}`, "app_ids must list 1 to 200 apps"},
		{"invalid id", `{"app_ids":["Bad-App"]}`, "invalid app_id"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			service := &appidentity.Service{}

			recorder := serveIdentity(service, nil, http.MethodPost, identityBase+"/bindings/acknowledge-all", testCase.body)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"`+testCase.want+`"}`+"\n")
		})
	}
}
