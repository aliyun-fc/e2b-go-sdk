package e2b

import (
	"strconv"
	"strings"
)

func compareVersion(a, b string) int {
	ap := parseVersion(a)
	bp := parseVersion(b)
	maxLen := len(ap)
	if len(bp) > maxLen {
		maxLen = len(bp)
	}
	for len(ap) < maxLen {
		ap = append(ap, 0)
	}
	for len(bp) < maxLen {
		bp = append(bp, 0)
	}
	for i := 0; i < maxLen; i++ {
		if ap[i] < bp[i] {
			return -1
		}
		if ap[i] > bp[i] {
			return 1
		}
	}
	return 0
}

func parseVersion(v string) []int {
	v = strings.TrimSpace(v)
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			out = append(out, 0)
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return []int{0}
	}
	return out
}
