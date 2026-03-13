package query

// LevenshteinDistance computes the edit distance between two strings.
func LevenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)

	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}

	return prev[lb]
}

// SuggestField returns the closest field name from candidates for a given input.
// Returns "" if no close match (distance > 3).
func SuggestField(input string, candidates []string) string {
	best := ""
	bestDist := 4 // threshold

	for _, c := range candidates {
		d := LevenshteinDistance(input, c)
		if d < bestDist {
			bestDist = d
			best = c
		}
	}

	return best
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
