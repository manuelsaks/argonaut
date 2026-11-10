package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/darksworm/argonaut/pkg/model"
)

func TestApplicationSetListIncludesEmptySetsAndSameNamesAcrossNamespaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/applicationsets" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Query().Get("appsetNamespace") != "" {
			t.Error("AppSets must come from the selected Argo CD API across permitted namespaces")
		}
		if r.URL.RawQuery != "" {
			t.Error("ApplicationSetListQuery does not support a fields projection")
		}
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"empty","namespace":"team-a"}},{"metadata":{"name":"empty","namespace":"team-b"}}]}`)
	}))
	defer server.Close()
	sets, err := NewApplicationService(&model.Server{BaseURL: server.URL, Token: "fixture-token"}).ListApplicationSets(t.Context())
	if err != nil || len(sets) != 2 || sets[0].Key() == sets[1].Key() {
		t.Fatalf("lost or merged AppSets: %+v, %v", sets, err)
	}
}

func TestUserInfoHTTP200IsNotProofOfAuthentication(t *testing.T) {
	for _, body := range []string{`{"loggedIn":false}`, `{"loggedIn":false,"username":"anonymous"}`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			if _, err := NewApplicationService(&model.Server{BaseURL: server.URL}).GetUserInfo(t.Context()); err == nil {
				t.Fatal("anonymous/invalid user info must not validate a session")
			}
		})
	}
}
