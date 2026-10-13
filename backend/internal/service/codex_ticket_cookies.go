package service

import (
	"net/http"
	"strings"
)

const (
	codexOAILBCookie = "__oailb"
	codexCFLBCookie  = "__cflb"
)

type codexTicketRoutingCookies struct {
	oailb string
	cflb  string
}

func (cookies codexTicketRoutingCookies) header() string {
	return (&http.Cookie{Name: codexOAILBCookie, Value: cookies.oailb}).String() + "; " +
		(&http.Cookie{Name: codexCFLBCookie, Value: cookies.cflb}).String()
}

func isCodexTicketRoutingCookie(name string) bool {
	return name == codexOAILBCookie || name == codexCFLBCookie
}

// codexCookieValue accepts repeated identical values, but rejects conflicting
// values. An empty value is still present: a response can explicitly clear it.
func codexCookieValue(cookies []*http.Cookie, name string) (string, bool) {
	value, found := "", false
	for _, cookie := range cookies {
		if cookie.Name != name {
			continue
		}
		if found && value != cookie.Value {
			return "", false
		}
		value, found = cookie.Value, true
	}
	return value, found
}

// parseCodexTicketRoutingCookies is shared by harvesting, hydration and request
// snapshots. A usable ticket must carry both nonempty routing cookie values.
func parseCodexTicketRoutingCookies(header string) (codexTicketRoutingCookies, bool) {
	cookies, err := http.ParseCookie(strings.TrimSpace(header))
	if err != nil {
		return codexTicketRoutingCookies{}, false
	}
	oailb, oailbOK := codexCookieValue(cookies, codexOAILBCookie)
	cflb, cflbOK := codexCookieValue(cookies, codexCFLBCookie)
	return codexTicketRoutingCookies{oailb: oailb, cflb: cflb},
		oailbOK && cflbOK && strings.TrimSpace(oailb) != "" && strings.TrimSpace(cflb) != ""
}

func codexCookieHasRoutingCookies(header string) bool {
	_, ok := parseCodexTicketRoutingCookies(header)
	return ok
}

// codexResponseCookieValue inspects every Set-Cookie for the requested name,
// before any deduplication. Missing, malformed or conflicting values cannot
// prove a change; attributes and other cookie names do not participate.
func codexResponseCookieValue(headers http.Header, name string) (string, bool) {
	var cookies []*http.Cookie
	for _, line := range headers.Values("Set-Cookie") {
		cookieName, _, _ := strings.Cut(line, "=")
		if strings.TrimSpace(cookieName) != name {
			continue
		}
		cookie, err := http.ParseSetCookie(line)
		if err != nil {
			return "", false
		}
		cookies = append(cookies, cookie)
	}
	return codexCookieValue(cookies, name)
}

func extractOpenAICodexHarvestCookies(headers http.Header) string {
	filtered := make(http.Header)
	for _, line := range headers.Values("Set-Cookie") {
		name, _, _ := strings.Cut(line, "=")
		if isCodexTicketRoutingCookie(strings.TrimSpace(name)) {
			filtered.Add("Set-Cookie", line)
		}
	}
	cookie := extractOpenAICodexResponseCookies(filtered)
	routing, valid := parseCodexTicketRoutingCookies(cookie)
	if !valid {
		// Preserve partial credentials for attempt diagnostics; the shared
		// validity check will reject them as missing_credentials.
		return cookie
	}
	// The generic extractor keeps the first cookie of each name. Check the raw
	// response too so conflicting or malformed duplicates cannot become a ticket.
	for _, name := range []string{codexOAILBCookie, codexCFLBCookie} {
		if _, ok := codexResponseCookieValue(headers, name); !ok {
			return ""
		}
	}
	return routing.header()
}
