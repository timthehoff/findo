package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// HostGuard defends the API against web pages in the user's browser, which can
// reach a server on the home network even though the user never meant them to:
//
//   - DNS rebinding: an attacker's page gets its own domain pointed at this
//     machine's address and then talks to it as "same origin", reading
//     everything. The browser still sends the attacker's domain as the Host, so
//     requests whose Host isn't a name this server is meant to be reached by are
//     refused.
//   - Cross-site requests: a page on another origin can submit a form-style POST
//     (JSON bodies are parsed whatever their Content-Type) without a preflight.
//     Browsers always send an Origin on such requests, so a state-changing
//     request whose Origin isn't this server's own Host is refused.
//
// Requests with no Origin (curl, scripts, the browser's own navigation) pass the
// second check; this is browser-confused-deputy protection, not a login.
func HostGuard(next http.Handler, extraHosts []string) http.Handler {
	extra := map[string]bool{}
	for _, h := range extraHosts {
		if h = normalizeHost(h); h != "" {
			extra[h] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, extra) {
			writeError(w, http.StatusForbidden, "this Host header isn't allowed; if you reach Findo by this name, list it in FINDO_ALLOWED_HOSTS")
			return
		}
		if !isSafeMethod(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
				writeError(w, http.StatusForbidden, "cross-site request refused")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// privateSuffixes are name endings that only resolve on a local network or
// this machine, so an attacker can't point them at a victim from the public
// internet. (An attacker's rebinding domain is always a public name.)
var privateSuffixes = []string{".local", ".localhost", ".lan", ".home.arpa", ".internal", ".localdomain"}

func hostAllowed(hostport string, extra map[string]bool) bool {
	host := normalizeHost(hostport)
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil { // a rebinding attack can't make a browser send an IP literal as the Host
		return true
	}
	if !strings.Contains(host, ".") { // a single label (localhost, macstudio) only resolves locally
		return true
	}
	for _, s := range privateSuffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return extra[host]
}

// normalizeHost lowercases a Host value and strips the port, the brackets
// around an IPv6 literal, and a trailing dot.
func normalizeHost(hostport string) string {
	host := strings.TrimSpace(hostport)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// sameOrigin reports whether an Origin header names the same host (and port)
// the request was addressed to.
func sameOrigin(origin, requestHost string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" { // "null" and malformed origins
		return false
	}
	return strings.EqualFold(u.Host, requestHost)
}
