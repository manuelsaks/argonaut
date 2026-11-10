package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/darksworm/argonaut/pkg/model"
)

func (s *ApplicationService) ListDestinationServers(ctx context.Context) ([]model.DestinationServer, error) {
	data, err := s.client.Get(ctx, "/api/v1/clusters")
	if err != nil {
		return nil, err
	}
	var result struct {
		Items []model.DestinationServer `json:"items"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode destination servers: %w", err)
	}
	return result.Items, nil
}
