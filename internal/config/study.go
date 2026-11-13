package config

// StudySourceEnabled reports whether one canonical source is on for this
// template. An empty source list selects DefaultStudySources.
func (t Template) StudySourceEnabled(source string) bool {
	if len(t.Study.Sources) == 0 {
		for _, name := range DefaultStudySources {
			if name == source {
				return true
			}
		}
		return false
	}
	for _, name := range t.Study.Sources {
		if name == source {
			return true
		}
	}
	return false
}

func studyRedditEnabled(sources []string) bool {
	if len(sources) == 0 {
		return false
	}
	for _, name := range sources {
		if name == "reddit_localllama" || name == "reddit_openai" || name == "reddit_singularity" {
			return true
		}
	}
	return false
}
