package stdx

import "strings"

// MaxThreadsPostRunes is the most characters the Threads platform accepts in
// one post or reply.
const MaxThreadsPostRunes = 500

// ThreadsHost reports whether host is a Threads web host (threads.com or the
// older threads.net, with or without www.) and returns its canonical form,
// threads.com, so one post has one address.
func ThreadsHost(host string) (string, bool) {
	switch strings.TrimPrefix(strings.ToLower(host), "www.") {
	case "threads.com", "threads.net":
		return "threads.com", true
	}
	return "", false
}
