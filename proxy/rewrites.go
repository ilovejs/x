package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/pkg/errors"
)

type compressableBody struct {
	buf bytes.Buffer
	w   io.WriteCloser
}

func (b *compressableBody) Write(d []byte) (int, error) {
	if b == nil {
		// this happens when the body is empty
		return 0, nil
	}

	var w io.Writer = &b.buf
	if b.w != nil {
		w = b.w
		defer b.w.Close()
	}
	return w.Write(d)
}

func (b *compressableBody) Read(p []byte) (n int, err error) {
	if b == nil {
		// this happens when the body is empty
		return 0, io.EOF
	}
	return b.buf.Read(p)
}

func headerRequestRewrite(req *http.Request, c *HostConfig) {
	req.URL.Scheme = c.UpstreamScheme
	req.URL.Host = c.UpstreamHost
	req.URL.Path = strings.TrimPrefix(req.URL.Path, c.PathPrefix)

	if _, ok := req.Header["User-Agent"]; !ok {
		// explicitly disable User-Agent so it's not set to default value
		req.Header.Set("User-Agent", "")
	}
}

func headerResponseRewrite(resp *http.Response, c *HostConfig) error {
	redir, err := resp.Location()
	if err != nil {
		if !errors.Is(err, http.ErrNoLocation) {
			return errors.WithStack(err)
		}
	} else if redir.Host == c.TargetHost {
		redir.Scheme = c.originalScheme
		redir.Host = c.originalHost
		redir.Path = path.Join(c.PathPrefix, redir.Path)
		resp.Header.Set("Location", redir.String())
	}

	ReplaceCookieDomain(resp, c.TargetHost, c.CookieDomain)

	return nil
}

// ReplaceCookieDomain replaces the domain of all matching Set-Cookie headers in the response.
func ReplaceCookieDomain(resp *http.Response, original, replacement string) {
	original, replacement = stripPort(original), stripPort(replacement) // cookies don't distinguish ports

	cookies := resp.Cookies()
	resp.Header.Del("Set-Cookie")
	for _, co := range cookies {
		if strings.EqualFold(co.Domain, original) {
			co.Domain = replacement
		}
		resp.Header.Add("Set-Cookie", co.String())
	}
}

func bodyResponseRewrite(resp *http.Response, c *HostConfig) ([]byte, *compressableBody, error) {
	if resp.ContentLength == 0 {
		return nil, nil, nil
	}

	body, cb, err := readBody(resp.Header, resp.Body)
	if err != nil {
		return nil, nil, err
	}

	return bytes.ReplaceAll(body, []byte(c.UpstreamScheme+"://"+c.TargetHost), []byte(c.originalScheme+"://"+c.originalHost+c.PathPrefix)), cb, nil
}

func readBody(h http.Header, body io.ReadCloser) ([]byte, *compressableBody, error) {
	defer body.Close()

	cb := &compressableBody{}

	switch h.Get("Content-Encoding") {
	case "gzip":
		var err error
		body, err = gzip.NewReader(body)
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}

		cb.w = gzip.NewWriter(&cb.buf)
	default:
		// do nothing, we can read directly
	}

	b, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	return b, cb, nil
}

// stripPort removes the optional port from the host.
func stripPort(host string) string {
	return (&url.URL{Host: host}).Hostname()
}
