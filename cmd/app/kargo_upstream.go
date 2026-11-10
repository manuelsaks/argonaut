package main

import (
	"slices"
	"strings"

	"github.com/darksworm/argonaut/pkg/kargo"
)

// Match the current Freight of configured upstream Stages, by requested origin.
// Never substitute older history, an arbitrary verified Freight or another
// Warehouse's Freight. Eligibility is still decided by Kargo's stage query/API.
func kargoUpstreamFreight(stages []kargo.Resource, project, target string) map[string]string {
	byName := make(map[string]kargo.Resource)
	for _, stage := range stages {
		if stage.Kind == "Stage" && stage.Project == project {
			byName[stage.Name] = stage
		}
	}
	requested, _ := byName[target].Value("spec.requestedFreight").([]any)
	sources := make(map[string][]string)
	for _, value := range requested {
		request, _ := value.(map[string]any)
		origin, _ := request["origin"].(map[string]any)
		originKind, _ := origin["kind"].(string)
		originName, _ := origin["name"].(string)
		from, _ := request["sources"].(map[string]any)
		names, _ := from["stages"].([]any)
		for _, name := range names {
			stageName, _ := name.(string)
			stage, ok := byName[stageName]
			if !ok {
				continue
			}
			history, _ := stage.Value("status.freightHistory").([]any)
			if len(history) == 0 {
				continue
			}
			current, _ := history[0].(map[string]any)
			items, _ := current["items"].(map[string]any)
			for _, item := range items {
				ref, _ := item.(map[string]any)
				refOrigin, _ := ref["origin"].(map[string]any)
				refKind, _ := refOrigin["kind"].(string)
				refName, _ := refOrigin["name"].(string)
				freight, _ := ref["name"].(string)
				if freight == "" || originKind == "" || originName == "" || refKind != originKind || refName != originName {
					continue
				}
				if !slices.Contains(sources[freight], stageName) {
					sources[freight] = append(sources[freight], stageName)
				}
			}
		}
	}
	result := make(map[string]string, len(sources))
	for freight, names := range sources {
		slices.Sort(names)
		result[freight] = strings.Join(names, ", ")
	}
	return result
}
