package main

import "charm.land/lipgloss/v2"

type kargoColumn struct {
	Index, Width int
	Header       string
}

func kargoTableColumns(kind string, width int, rows []kargoRow) []kargoColumn {
	headers := []string{"NAME", "STATUS", "DETAILS"}
	switch kind {
	case "Connection":
		headers = []string{"INSTANCE", "LOGIN", "API SERVER"}
	case "Stage":
		headers = []string{"STAGE", "STATUS", "FREIGHT", "PROMOTION", "MESSAGE"}
	case "Promotion":
		headers = []string{"PROMOTION", "PHASE", "STAGE", "REVISION / ARTIFACT", "CREATED", "DURATION", "FREIGHT", "MESSAGE"}
	case "Warehouse":
		headers = []string{"WAREHOUSE", "STATUS", "LATEST FREIGHT", "SOURCES", "MESSAGE"}
	case "Freight":
		headers = []string{"FREIGHT", "ALIAS", "WAREHOUSE", "ARTIFACTS"}
	}
	var indices []int
	for i := range headers {
		indices = append(indices, i)
	}
	if kind == "Promotion" {
		switch {
		case width < 85:
			indices = []int{0, 1, 3, 4}
		case width < 130:
			indices = []int{0, 1, 2, 3, 4}
		case width < 180:
			indices = []int{0, 1, 2, 3, 4, 5, 7}
		}
	} else if width < 75 {
		indices = indices[:min(3, len(indices))]
	}
	if width < 35 {
		indices = indices[:min(2, len(indices))]
	}
	desired, minimum, priority := make([]int, len(indices)), make([]int, len(indices)), make([]int, len(indices))
	columns := make([]kargoColumn, len(indices))
	for i, index := range indices {
		header := headers[index]
		if kind == "Promotion" && index == 3 && width < 130 {
			header = "REVISION"
		}
		desired[i] = lipgloss.Width(header)
		for _, row := range rows {
			if index < len(row.Columns) {
				desired[i] = max(desired[i], lipgloss.Width(kargoSingleLine(row.Columns[index])))
			}
		}
		minimum[i], priority[i] = min(desired[i], max(7, lipgloss.Width(header))), 1
		if index == 0 {
			minimum[i], priority[i] = min(18, desired[i]), 8
		} else if index == 1 && kind != "Freight" {
			minimum[i] = min(12, desired[i])
		} else if kind == "Promotion" && index == 3 {
			minimum[i], priority[i] = min(12, desired[i]), 4
		} else if kind == "Promotion" && index == 4 {
			minimum[i] = min(16, desired[i])
		}
		columns[i] = kargoColumn{Index: index, Header: header}
	}
	widths := fitTableColumns(width-2*(len(indices)-1), desired, minimum, priority)
	for i := range columns {
		columns[i].Width = widths[i]
	}
	return columns
}
