package wray

// check if a string is contained in a slice of strings
func contains(target string, slice []string) bool {
	for _, t := range slice {
		if t == target {
			return true
		}
	}
	return false
}
