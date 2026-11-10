package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/darksworm/argonaut/pkg/model"
)

// ListApplicationSets asks Argo CD directly; ownerReferences cannot discover
// sets with no generated Applications. Only identities are retained in memory;
// callers cache the registry because this endpoint has no fields projection.
func (s *ApplicationService) ListApplicationSets(ctx context.Context) ([]model.ApplicationSet, error) {
	data, err := s.client.Get(ctx, "/api/v1/applicationsets")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decode ApplicationSet list: %w", err)
	}
	sets := make([]model.ApplicationSet, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Metadata.Name != "" {
			sets = append(sets, model.ApplicationSet{Name: item.Metadata.Name, Namespace: item.Metadata.Namespace})
		}
	}
	return sets, nil
}
