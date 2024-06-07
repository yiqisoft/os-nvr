package url

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Errors.
var (
	ErrURLunsupportedScheme = errors.New("unsupported scheme")
	ErrURLopaque            = errors.New("URLs with opaque data are not supported")
	ErrURLfragments         = errors.New("URLs with fragments are not supported")
)

// URL is a RTSP URL.
// This is basically an HTTP URL with some additional functions to handle
// control attributes.
type URL url.URL

var escapeRegexp = regexp.MustCompile(`^(.+?)://(.*?)@(.*?)/(.*?)$`)

// Parse parses a RTSP URL.
func Parse(s string) (*URL, error) {
	// https://github.com/golang/go/issues/30611
	m := escapeRegexp.FindStringSubmatch(s)
	if m != nil {
		m[3] = strings.ReplaceAll(m[3], "%25", "%")
		m[3] = strings.ReplaceAll(m[3], "%", "%25")
		s = m[1] + "://" + m[2] + "@" + m[3] + "/" + m[4]
	}

	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}

	if u.Scheme != "rtsp" && u.Scheme != "rtsps" {
		return nil, fmt.Errorf("%w '%s'", ErrURLunsupportedScheme, u.Scheme)
	}

	if u.Opaque != "" {
		return nil, ErrURLopaque
	}

	if u.Fragment != "" {
		return nil, ErrURLfragments
	}

	return (*URL)(u), nil
}

// String implements fmt.Stringer.
func (u *URL) String() string {
	return (*url.URL)(u).String()
}

// Clone clones a URL.
func (u *URL) Clone() *URL {
	return (*URL)(&url.URL{
		Scheme:     u.Scheme,
		User:       u.User,
		Host:       u.Host,
		Path:       u.Path,
		RawPath:    u.RawPath,
		ForceQuery: u.ForceQuery,
		RawQuery:   u.RawQuery,
	})
}

// CloneWithoutCredentials clones a URL without its credentials.
func (u *URL) CloneWithoutCredentials() *URL {
	return (*URL)(&url.URL{
		Scheme:     u.Scheme,
		Host:       u.Host,
		Path:       u.Path,
		RawPath:    u.RawPath,
		ForceQuery: u.ForceQuery,
		RawQuery:   u.RawQuery,
	})
}

// RTSPPath returns the path of a RTSP URL.
func (u *URL) RTSPPath() (string, bool) {
	var pathAndQuery string
	if u.RawPath != "" {
		pathAndQuery = u.RawPath
	} else {
		pathAndQuery = u.Path
	}
	if u.RawQuery != "" {
		pathAndQuery += "?" + u.RawQuery
	}

	// remove leading slash
	if len(pathAndQuery) == 0 || pathAndQuery[0] != '/' {
		return "", false
	}
	pathAndQuery = pathAndQuery[1:]

	path := removeQuery(pathAndQuery)

	return path, true
}

func removeQuery(pathAndQuery string) string {
	i := strings.Index(pathAndQuery, "?")
	if i >= 0 {
		return pathAndQuery[:i]
	}
	return pathAndQuery
}
