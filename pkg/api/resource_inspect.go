package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	apperrors "github.com/darksworm/argonaut/pkg/errors"
)

// ResourceQuery binds a Kubernetes object to its owning Argo CD Application.
// Application namespace and resource namespace are deliberately separate.
type ResourceQuery struct {
	AppName, AppNamespace                 string
	Group, Version, Kind, Namespace, Name string
}

type ResourceDocument struct {
	Manifest json.RawMessage
	Desired  bool
}

func (s *ApplicationService) GetApplicationSetObject(ctx context.Context, name, namespace string) (json.RawMessage, error) {
	if name == "" {
		return nil, fmt.Errorf("ApplicationSet name is required")
	}
	path := "/api/v1/applicationsets/" + url.PathEscape(name)
	if namespace != "" {
		path += "?appsetNamespace=" + url.QueryEscape(namespace)
	}
	data, err := s.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid ApplicationSet response")
	}
	return data, nil
}

// GetApplicationObject preserves fields omitted by the typed application model.
func (s *ApplicationService) GetApplicationObject(ctx context.Context, name, appNamespace string) (json.RawMessage, error) {
	if name == "" {
		return nil, fmt.Errorf("application name is required")
	}
	path := fmt.Sprintf("/api/v1/applications/%s", url.PathEscape(name))
	if appNamespace != "" {
		path += "?appNamespace=" + url.QueryEscape(appNamespace)
	}
	data, err := s.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid application object response")
	}
	return data, nil
}

// GetResourceObject reads the full live object, including descendants such as
// Pods. Only a 404 falls back to the exact desired object; RBAC errors do not.
func (s *ApplicationService) GetResourceObject(ctx context.Context, target ResourceQuery) (*ResourceDocument, error) {
	if target.AppName == "" || target.Kind == "" || target.Name == "" {
		return nil, fmt.Errorf("application, resource kind and resource name are required")
	}
	query := url.Values{
		"resourceName": {target.Name}, "namespace": {target.Namespace},
		"group": {target.Group}, "version": {target.Version}, "kind": {target.Kind},
	}
	if target.AppNamespace != "" {
		query.Set("appNamespace", target.AppNamespace)
	}
	path := fmt.Sprintf("/api/v1/applications/%s/resource?%s", url.PathEscape(target.AppName), query.Encode())
	data, err := s.client.Get(ctx, path)
	if err != nil {
		if apiError, ok := errors.AsType[*apperrors.ArgonautError](err); ok && apiError != nil && apiError.Code == "NOT_FOUND" {
			return s.getDesiredResourceObject(ctx, target)
		}
		return nil, err
	}
	var response struct {
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("invalid resource response: %w", err)
	}
	if !json.Valid([]byte(response.Manifest)) || response.Manifest == "null" {
		return nil, fmt.Errorf("Argo CD returned no resource object")
	}
	return &ResourceDocument{Manifest: json.RawMessage(response.Manifest)}, nil
}

func (s *ApplicationService) getDesiredResourceObject(ctx context.Context, target ResourceQuery) (*ResourceDocument, error) {
	items, err := s.GetManagedResourceDiffs(ctx, target.AppName, target.AppNamespace)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.Group == target.Group && item.Kind == target.Kind && item.Namespace == target.Namespace && item.Name == target.Name {
			if item.TargetState != "null" && json.Valid([]byte(item.TargetState)) {
				return &ResourceDocument{Manifest: json.RawMessage(item.TargetState), Desired: true}, nil
			}
		}
	}
	return nil, fmt.Errorf("resource has no live or desired object available through Argo CD")
}
