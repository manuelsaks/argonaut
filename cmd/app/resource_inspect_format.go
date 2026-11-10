package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/darksworm/argonaut/pkg/model"
	"gopkg.in/yaml.v3"
)

func decodeInspectionObject(raw json.RawMessage, application bool) (map[string]any, error) {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("decode resource object: %w", err)
	}
	if object == nil {
		return nil, fmt.Errorf("Argo CD returned an empty resource object")
	}
	if application {
		if object["apiVersion"] == nil {
			object["apiVersion"] = "argoproj.io/v1alpha1"
		}
		if object["kind"] == nil {
			object["kind"] = "Application"
		}
	}
	return object, nil
}

// Round-trip through a YAML node rather than interface{} to retain JSON number
// precision, all metadata/status fields, and YAML quoting of ambiguous strings.
func inspectionYAML(object map[string]any) (string, error) {
	raw, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return "", err
	}
	var block func(*yaml.Node)
	block = func(n *yaml.Node) {
		n.Style = 0
		if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && strings.Contains(n.Value, "\n") {
			n.Style = yaml.LiteralStyle
		}
		for _, child := range n.Content {
			block(child)
		}
	}
	block(&node)
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&node); err != nil {
		return "", err
	}
	return out.String(), nil
}

func describeInspectionObject(object map[string]any, desired bool, events []model.ResourceEvent, notice string) string {
	var out strings.Builder
	metadata, _ := object["metadata"].(map[string]any)
	for _, field := range []struct {
		label string
		value any
	}{
		{"Name", metadata["name"]}, {"Namespace", metadata["namespace"]},
		{"Kind", object["kind"]}, {"API version", object["apiVersion"]},
		{"UID", metadata["uid"]}, {"Created", metadata["creationTimestamp"]},
	} {
		if field.value != nil {
			fmt.Fprintf(&out, "%-15s %v\n", field.label+":", field.value)
		}
	}
	if desired {
		out.WriteString("State:          Desired manifest (resource is missing from the cluster)\n")
	}
	for _, key := range []string{"labels", "annotations", "ownerReferences", "finalizers"} {
		if value := metadata[key]; value != nil {
			fmt.Fprintf(&out, "\n%s:\n", describeLabel(key))
			describeValue(&out, value, 2)
		}
	}
	for _, key := range sortedObjectKeys(object) {
		if key == "apiVersion" || key == "kind" || key == "metadata" {
			continue
		}
		fmt.Fprintf(&out, "\n%s:\n", describeLabel(key))
		if object["kind"] == "Secret" && (key == "data" || key == "stringData") {
			if data, ok := object[key].(map[string]any); ok {
				for _, name := range sortedObjectKeys(data) {
					fmt.Fprintf(&out, "  %s: <redacted; use YAML to inspect>\n", name)
				}
			}
			continue
		}
		describeValue(&out, object[key], 2)
	}
	out.WriteString("\nEvents:\n")
	if notice != "" {
		out.WriteString("  " + notice + "\n")
	} else if len(events) == 0 {
		out.WriteString("  No events.\n")
	} else {
		out.WriteString("  TYPE     REASON  COUNT  LAST SEEN  MESSAGE\n")
		for _, event := range events {
			fmt.Fprintf(&out, "  %s  %s  %d  %s  %s\n", event.Type, event.Reason, event.Count, event.LastSeen.Format("2006-01-02 15:04:05"), event.Message)
		}
	}
	return out.String()
}

func sortedObjectKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func describeLabel(key string) string {
	var out strings.Builder
	for i, r := range key {
		if i == 0 {
			r = unicode.ToUpper(r)
		} else if unicode.IsUpper(r) {
			out.WriteByte(' ')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func describeValue(out *strings.Builder, value any, indent int) {
	padding := strings.Repeat(" ", indent)
	switch value := value.(type) {
	case map[string]any:
		if len(value) == 0 {
			out.WriteString(padding + "<none>\n")
		}
		for _, key := range sortedObjectKeys(value) {
			fmt.Fprintf(out, "%s%s:", padding, describeLabel(key))
			switch value[key].(type) {
			case map[string]any, []any:
				out.WriteByte('\n')
				describeValue(out, value[key], indent+2)
			default:
				fmt.Fprintf(out, " %v\n", value[key])
			}
		}
	case []any:
		if len(value) == 0 {
			out.WriteString(padding + "<none>\n")
		}
		for _, item := range value {
			out.WriteString(padding + "-\n")
			describeValue(out, item, indent+2)
		}
	default:
		fmt.Fprintf(out, "%s%v\n", padding, value)
	}
}
