package main

import (
	"fmt"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/darksworm/argonaut/pkg/kargo"
)

func kargoTimestamp(resource kargo.Resource, field string) string {
	value := resource.String(field)
	if value == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return kargoSingleLine(value)
	}
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05 MST")
}

func kargoPromotionCreated(resource kargo.Resource) string {
	t, err := time.Parse(time.RFC3339Nano, resource.String("metadata.creationTimestamp"))
	if err != nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func kargoPromotionDuration(resource kargo.Resource, now time.Time) string {
	started, err := time.Parse(time.RFC3339Nano, resource.String("status.startedAt"))
	if err != nil || started.IsZero() {
		return "-"
	}
	finished, err := time.Parse(time.RFC3339Nano, resource.String("status.finishedAt"))
	if err != nil || finished.IsZero() {
		if resource.Phase() != "Running" {
			return "-"
		}
		finished = now
	}
	return max(time.Duration(0), finished.Sub(started)).Round(time.Second).String()
}

func kargoShortCommit(id string) string {
	if len(id) <= 12 {
		return id
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return id
		}
	}
	return id[:12]
}

func kargoArtifactName(repo, fallback string) string {
	if u, err := url.Parse(repo); err == nil {
		repo = u.Path
	}
	name := strings.TrimSuffix(path.Base(strings.TrimRight(repo, "/")), ".git")
	if name == "." || name == "" {
		return fallback
	}
	return name
}

// Promotion status embeds the Freight's immutable artifact references. Read
// those rather than issuing a Freight request for every row over a slow VPN.
func kargoPromotionRevision(resource kargo.Resource) string {
	wanted := resource.String("spec.freight", "status.freight.name")
	var references []map[string]any
	appendReference := func(value any) {
		if reference, ok := value.(map[string]any); ok {
			name, _ := reference["name"].(string)
			if name == "" || wanted == "" || name == wanted {
				references = append(references, reference)
			}
		}
	}
	appendReference(resource.Value("status.freight"))
	if freight, ok := resource.Value("status.freightCollection.freight").(map[string]any); ok {
		for _, key := range slices.Sorted(maps.Keys(freight)) {
			appendReference(freight[key])
		}
	}
	var revisions []string
	add := func(value string) {
		value = kargoSingleLine(value)
		if value != "" && !slices.Contains(revisions, value) {
			revisions = append(revisions, value)
		}
	}
	for _, reference := range references {
		for _, kind := range []string{"commits", "images", "charts"} {
			artifacts, _ := reference[kind].([]any)
			for _, value := range artifacts {
				object, _ := value.(map[string]any)
				get := func(key string) string { value, _ := object[key].(string); return value }
				switch kind {
				case "commits":
					id := kargoShortCommit(get("id"))
					if id == "" {
						continue
					}
					if tag := get("tag"); tag != "" {
						id = tag + " (" + id + ")"
					}
					add(kargoArtifactName(get("repoURL"), "git") + "@" + id)
				case "images":
					name := kargoArtifactName(get("repoURL"), "image")
					if tag := get("tag"); tag != "" {
						add(name + ":" + tag)
					} else if digest := get("digest"); digest != "" {
						add(name + "@" + digest)
					}
				case "charts":
					if version := get("version"); version != "" {
						name := get("name")
						if name == "" {
							name = "chart"
						}
						add(name + "@" + version)
					}
				}
			}
		}
	}
	if len(revisions) == 0 {
		return "-"
	}
	return strings.Join(revisions, " | ")
}

func (m *Model) kargoPromotionSummary(resource kargo.Resource) []string {
	return []string{
		"Promotion: " + resource.Name + " | Stage: " + resource.String("spec.stage"),
		"Revision: " + kargoPromotionRevision(resource) + " | Freight: " + resource.String("spec.freight", "status.freight.name"),
		fmt.Sprintf("Created: %s | Duration: %s | Started: %s | Finished: %s", kargoTimestamp(resource, "metadata.creationTimestamp"), kargoPromotionDuration(resource, m.now()), kargoTimestamp(resource, "status.startedAt"), kargoTimestamp(resource, "status.finishedAt")),
	}
}
