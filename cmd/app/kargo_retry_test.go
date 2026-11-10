package main

import (
	"testing"

	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
)

func failedKargoPromotion(t *testing.T, m *Model, phase string) kargo.Resource {
	t.Helper()
	resource, err := kargo.DecodeResource([]byte(`{"kind":"Promotion","metadata":{"name":"promo-1","namespace":"team-b"},"spec":{"stage":"prod","freight":"freight-a"},"status":{"phase":"`+phase+`"}}`), "Promotion")
	if err != nil {
		t.Fatal(err)
	}
	m.kargo.Kind, m.kargo.Project, m.kargo.Stage = "Promotion", "team-b", "prod"
	m.kargo.Items, m.kargo.Cursor = []kargo.Resource{resource}, 0
	return resource
}

func TestKargoRetryFromListAndDescribeRequiresExplicitConfirmation(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, phase := range []string{"Failed", "Errored", "Aborted"} {
			t.Run(phase+map[bool]string{true: "/describe", false: "/list"}[detail], func(t *testing.T) {
				m, fixture := newKargoFixture(t)
				fixture.Phase.Store(phase)
				resource := failedKargoPromotion(t, m, phase)
				if detail {
					m.showKargoDocument(resource, "Describe", false)
				}
				kargoKey(t, m, "ctrl+t")
				if !m.kargo.Confirm || m.kargo.Draft.RetryOf != "promo-1" || m.state.Diff != nil || fixture.Promotions.Load() != 0 {
					t.Fatal("retry must show the exact target without submitting")
				}
				kargoKey(t, m, "enter")
				kargoKey(t, m, "ctrl+p")
				if fixture.Promotions.Load() != 0 || !m.kargo.Confirm {
					t.Fatal("only Ctrl+T may confirm a retry")
				}
				_, cmd := m.handleKeyMsg(testKeyMsg("ctrl+t"))
				if !m.kargo.Executing || cmd == nil {
					t.Fatal("confirmed retry did not start")
				}
				_, duplicate := m.handleKeyMsg(testKeyMsg("ctrl+t"))
				if duplicate != nil {
					t.Fatal("repeated confirm scheduled a duplicate POST")
				}
				driveKargo(t, m, cmd)
				if fixture.Promotions.Load() != 1 || fixture.LastProject.Load() != "team-b" || m.kargo.Detail == nil || m.kargo.Detail.Phase() != "Running" {
					t.Fatal("retry must create one same-Freight promotion in the chosen project and open its status")
				}
			})
		}
	}
}

func TestKargoRetryRejectsActiveSuccessfulAndChangedPromotions(t *testing.T) {
	for _, phase := range []string{"Running", "Pending", "Succeeded"} {
		t.Run(phase, func(t *testing.T) {
			m, fixture := newKargoFixture(t)
			failedKargoPromotion(t, m, phase)
			m.handleKeyMsg(testKeyMsg("ctrl+t"))
			if m.kargo.Confirm || fixture.Promotions.Load() != 0 {
				t.Fatal("non-retryable promotion was accepted")
			}
		})
	}
	m, fixture := newKargoFixture(t)
	failedKargoPromotion(t, m, "Failed")
	kargoKey(t, m, "ctrl+t")
	fixture.Phase.Store("Running") // Changed while the confirmation was open.
	kargoKey(t, m, "ctrl+t")
	if fixture.Promotions.Load() != 0 || m.kargo.Error == "" {
		t.Fatal("a stale failure must be revalidated before creating a retry")
	}
	if m.state.Mode != model.ModeNormal {
		t.Fatal("failed revalidation should leave the list usable")
	}
}
