package cmd

// forkUpdateCheck reports whether the check for a newer evcc release runs. It does
// not in the fork: updates come with the add-on and the release check of the project,
// and the fork's version (v0.317.0-lm4) is below evcc's own release by semver, so the
// hint would offer evcc's release as an update.
func forkUpdateCheck() bool {
	return false
}
