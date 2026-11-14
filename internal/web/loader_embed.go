package web

import (
	"net/http"
	"net/url"
	"strings"
)

func loaderEmbedded(r *http.Request) bool {
	return r.URL.Path == "/loader/queue" || r.URL.Query().Get("embed") == "1" || r.Header.Get("Sec-Fetch-Dest") == "iframe"
}

func (s *Server) loaderQueueScreen(w http.ResponseWriter, r *http.Request) {
	gallery := r.URL.Query().Get("gallery")
	if !loaderGallery(gallery) {
		http.Error(w, "Choose a valid destination gallery in Mebooru.", http.StatusBadRequest)
		return
	}
	s.renderQueueScreen(w, r, gallery)
}

// The only permitted frame parent is the configured browser-facing library.
// Frame loads still use Meloader's own login and form protections.
func (s *Server) loaderFramePolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			parent := "'none'"
			if origin := loaderFrameOrigin(s.monbooruWebBase()); origin != "" {
				parent = origin
			}
			w.Header().Set("Content-Security-Policy", "frame-ancestors "+parent)
		}
		next.ServeHTTP(w, r)
	})
}

func loaderFrameOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ""
	}
	if strings.ContainsAny(u.Host, "* ;'\t\r\n") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Gallery names follow Mebooru's existing folder-name contract.
func loaderGallery(name string) bool {
	return name != "" && !strings.ContainsFunc(name, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	})
}
