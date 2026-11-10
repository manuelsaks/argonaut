package main

import "slices"

// fitTableColumns spends cells on real content, with predictable minimums and
// priorities when a terminal is narrow. Blank status/metadata cells never take
// space away from a name that could otherwise be shown in full.
func fitTableColumns(available int, desired, minimum, priority []int) []int {
	available = max(0, available)
	widths := make([]int, len(desired))
	total := 0
	for i, want := range desired {
		widths[i] = min(max(0, want), max(0, minimum[i]))
		total += widths[i]
	}
	for total > available {
		i := slices.Index(widths, slices.Max(widths))
		widths[i]--
		total--
	}
	for total < available {
		grew := false
		for i, want := range desired {
			for range max(1, priority[i]) {
				if widths[i] < want && total < available {
					widths[i]++
					total++
					grew = true
				}
			}
		}
		if !grew {
			break
		}
	}
	// Pad at the right edge, keeping short fixed columns next to their data.
	if len(widths) > 0 && total < available {
		widths[len(widths)-1] += available - total
	}
	return widths
}
