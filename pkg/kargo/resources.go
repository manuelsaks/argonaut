package kargo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Resource struct {
	Raw                 json.RawMessage
	Object              map[string]any
	Kind, Name, Project string
}

func DecodeResource(raw json.RawMessage, kind string) (Resource, error) {
	r := Resource{Raw: append(json.RawMessage(nil), raw...), Kind: kind}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&r.Object); err != nil || r.Object == nil {
		return r, fmt.Errorf("invalid Kargo resource")
	}
	r.Name = r.String("metadata.name")
	r.Project = r.String("metadata.namespace")
	if actual := r.String("kind"); actual != "" {
		if actual != kind {
			return r, fmt.Errorf("unexpected Kargo resource kind")
		}
		r.Kind = actual
	}
	if r.Name == "" {
		return r, fmt.Errorf("Kargo resource has no name")
	}
	if err := validateNames(r.Name); err != nil {
		return r, err
	}
	if r.Project != "" {
		if err := validateNames(r.Project); err != nil {
			return r, err
		}
	}
	return r, nil
}

func bindResource(resource Resource, project, name string) (Resource, error) {
	if project != "" {
		if resource.Project != "" && resource.Project != project {
			return Resource{}, fmt.Errorf("Kargo returned a resource from another project")
		}
		resource.Project = project
	}
	if name != "" && resource.Name != name {
		return Resource{}, fmt.Errorf("Kargo returned another resource")
	}
	return resource, nil
}

func (r Resource) Value(path string) any {
	var value any = r.Object
	for _, key := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

func (r Resource) String(paths ...string) string {
	for _, path := range paths {
		if s, ok := r.Value(path).(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func (r Resource) Phase() string {
	if phase := r.String("status.phase", "status.health.status"); phase != "" {
		return phase
	}
	if r.Value("status.currentPromotion") != nil {
		return "Promoting"
	}
	if conditions, ok := r.Value("status.conditions").([]any); ok {
		for _, value := range conditions {
			condition, _ := value.(map[string]any)
			kind, _ := condition["type"].(string)
			status, _ := condition["status"].(string)
			if (kind == "Ready" || kind == "Healthy") && status == "False" {
				return "NotReady"
			}
			if (kind == "Ready" || kind == "Healthy") && status == "True" {
				return "Ready"
			}
		}
	}
	if r.Kind == "Project" || r.Kind == "Freight" {
		return "Available"
	}
	return "Unknown"
}

func (r Resource) Message() string {
	if message := r.String("status.message", "status.error", "status.health.message"); message != "" {
		return message
	}
	if conditions, ok := r.Value("status.conditions").([]any); ok {
		for _, value := range conditions {
			condition, _ := value.(map[string]any)
			if condition["status"] == "False" {
				if message, ok := condition["message"].(string); ok && message != "" {
					return message
				}
			}
		}
	}
	return ""
}

func SortResources(resources []Resource) {
	sort.SliceStable(resources, func(i, j int) bool {
		if resources[i].Kind == "Promotion" || resources[i].Kind == "Freight" {
			a, b := resources[i].String("metadata.creationTimestamp"), resources[j].String("metadata.creationTimestamp")
			if a != b {
				return a > b
			}
		}
		return resources[i].Name < resources[j].Name
	})
}
