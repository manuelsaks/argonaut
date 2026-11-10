package kargo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
)

// Unary Connect JSON is sufficient for the older Kargo API. No Kubernetes
// client, protobuf runtime, stream or additional dependencies are required.
const connectService = "/akuity.io.kargo.service.v1alpha1.KargoService/"

func (c *Client) UsesConnect() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version == "connect"
}

func (c *Client) rpc(ctx context.Context, method string, body any) ([]byte, error) {
	return c.request(ctx, http.MethodPost, connectService+method, body)
}

func (c *Client) listConnect(ctx context.Context, kind, project, stage, warehouse string) ([]Resource, error) {
	method := map[string]string{"Project": "ListProjects", "Stage": "ListStages", "Promotion": "ListPromotions", "Warehouse": "ListWarehouses", "Freight": "QueryFreight"}[kind]
	if method == "" {
		return nil, fmt.Errorf("unsupported Kargo kind")
	}
	body := map[string]any{}
	if kind == "Project" {
		body["pageSize"], body["page"] = 100, 0
	}
	if kind != "Project" {
		if err := validateNames(project); err != nil {
			return nil, err
		}
		body["project"] = project
	}
	if stage != "" {
		if err := validateNames(stage); err != nil {
			return nil, err
		}
		body["stage"] = stage
	}
	if warehouse != "" {
		if err := validateNames(warehouse); err != nil {
			return nil, err
		}
		body["origins"] = []string{warehouse}
	}
	data, err := c.rpc(ctx, method, body)
	if err != nil {
		return nil, err
	}
	if kind == "Project" {
		return c.collectConnectProjects(ctx, data)
	}
	resources, err := decodeList(data, kind)
	if err != nil {
		return nil, err
	}
	for i := range resources {
		resources[i], err = bindResource(resources[i], project, "")
		if err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func (c *Client) collectConnectProjects(ctx context.Context, data []byte) ([]Resource, error) {
	var resources []Resource
	seen := map[string]bool{}
	for page := 0; ; page++ {
		var envelope struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, err
		}
		items, err := decodeList(data, "Project")
		if err != nil {
			return nil, err
		}
		before := len(resources)
		for _, item := range items {
			if !seen[item.Name] {
				resources = append(resources, item)
				seen[item.Name] = true
			}
		}
		if len(resources) >= envelope.Total {
			SortResources(resources)
			return resources, nil
		}
		if len(resources) == before {
			return nil, fmt.Errorf("Kargo project pagination did not advance")
		}
		data, err = c.rpc(ctx, "ListProjects", map[string]int{"pageSize": 100, "page": page + 1})
		if err != nil {
			return nil, err
		}
	}
}

func (c *Client) getConnect(ctx context.Context, kind, project, name string) (Resource, error) {
	if kind != "Project" {
		if err := validateNames(project); err != nil {
			return Resource{}, err
		}
	}
	method := map[string]string{"Project": "GetProject", "Stage": "GetStage", "Promotion": "GetPromotion", "Warehouse": "GetWarehouse", "Freight": "GetFreight"}[kind]
	if method == "" {
		return Resource{}, fmt.Errorf("unsupported Kargo kind")
	}
	body := map[string]any{"name": name, "format": "RAW_FORMAT_JSON"}
	if kind != "Project" {
		body["project"] = project
	} else {
		body["name"] = name
	}
	data, err := c.rpc(ctx, method, body)
	if err != nil {
		return Resource{}, err
	}
	return decodeConnectResource(data, kind, project, name)
}

func decodeConnectResource(data []byte, kind, project, name string) (Resource, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Resource{}, err
	}
	var raw json.RawMessage
	if encoded, ok := envelope["raw"]; ok {
		var value string
		if err := json.Unmarshal(encoded, &value); err != nil {
			return Resource{}, err
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return Resource{}, fmt.Errorf("invalid Kargo raw object")
		}
		raw = decoded
	} else {
		key := map[string]string{"Project": "project", "Stage": "stage", "Promotion": "promotion", "Warehouse": "warehouse", "Freight": "freight"}[kind]
		raw = envelope[key]
	}
	resource, err := DecodeResource(raw, kind)
	if err != nil {
		return resource, err
	}
	if kind == "Project" {
		project = ""
	}
	return bindResource(resource, project, name)
}

func (c *Client) promoteConnect(ctx context.Context, project, stage, freight string) (Resource, error) {
	data, err := c.rpc(ctx, "PromoteToStage", map[string]string{"project": project, "stage": stage, "freight": freight})
	if err != nil {
		return Resource{}, fmt.Errorf("promotion response unavailable; check Promotions before trying again: %w", err)
	}
	return decodeConnectResource(data, "Promotion", project, "")
}
