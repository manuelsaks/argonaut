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
		if value := metadata[key]; !emptyDescribeValue(value) {
			if key == "annotations" {
				if annotations, ok := value.(map[string]any); ok {
					concise := map[string]any{}
					for k, v := range annotations {
						if k != "kubectl.kubernetes.io/last-applied-configuration" && !emptyDescribeValue(v) {
							concise[k] = v
						}
					}
					if len(concise) == 0 {
						continue
					}
					value = concise
				}
			}
			fmt.Fprintf(&out, "\n%s:\n", describeLabel(key))
			describeValue(&out, value, 2)
		}
	}
	for _, key := range sortedObjectKeys(object) {
		if key == "apiVersion" || key == "kind" || key == "metadata" || emptyDescribeValue(object[key]) {
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
	runes := []rune(key)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1]))) {
			out.WriteByte(' ')
		}
		if i == 0 {
			r = unicode.ToUpper(r)
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
			if key == "kubectl.kubernetes.io/last-applied-configuration" || emptyDescribeValue(value[key]) {
				continue
			}
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
			if emptyDescribeValue(item) {
				continue
			}
			var line strings.Builder
			describeValue(&line, item, indent+2)
			text := strings.TrimSuffix(line.String(), "\n")
			text = strings.TrimPrefix(text, strings.Repeat(" ", indent+2))
			out.WriteString(padding + "- " + text + "\n")
		}
	default:
		fmt.Fprintf(out, "%s%v\n", padding, value)
	}
}

func emptyDescribeValue(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case map[string]any:
		for _, v := range value {
			if !emptyDescribeValue(v) {
				return false
			}
		}
		return true
	case []any:
		for _, v := range value {
			if !emptyDescribeValue(v) {
				return false
			}
		}
		return true
	}
	return false
}
