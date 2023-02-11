package har

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

var _ http.RoundTripper = (*RoundTripper)(nil)

var DefaultOptions = &Options{
	Creator: &Creator{
		Name:    "github.com/aidansteele/har",
		Version: "0.1",
	},
	Rewrite: func(request *http.Request, response *http.Response, entry json.RawMessage) json.RawMessage {
		return entry
	},
}

type RoundTripper struct {
	inner  http.RoundTripper
	opts   *Options
	mut    sync.Mutex
	first  bool
	writer io.Writer
}

type Options struct {
	Rewrite func(request *http.Request, response *http.Response, entry json.RawMessage) json.RawMessage
	Creator *Creator
}

func New(roundTripper http.RoundTripper, w io.Writer, opts *Options) (*RoundTripper, error) {
	if roundTripper == nil {
		roundTripper = http.DefaultTransport
	}

	if opts == nil {
		opts = DefaultOptions
	}

	if opts.Rewrite == nil {
		opts.Rewrite = DefaultOptions.Rewrite
	}

	if opts.Creator == nil {
		opts.Creator = DefaultOptions.Creator
	}

	if opts.Creator.Name == "" {
		return nil, fmt.Errorf("Options.Creator.Name cannot be empty")
	}

	if opts.Creator.Version == "" {
		return nil, fmt.Errorf("Options.Creator.Version cannot be empty")
	}

	rt := &RoundTripper{
		inner:  roundTripper,
		opts:   opts,
		writer: w,
		first:  true,
	}

	err := rt.writePreamble()
	if err != nil {
		return nil, err
	}

	return rt, nil
}

func (rt *RoundTripper) writePreamble() error {
	var err error
	creatorJson, _ := json.Marshal(rt.opts.Creator)

	_, err = rt.writer.Write([]byte(`{"log":{"version":"1.2","creator":`))
	if err != nil {
		return fmt.Errorf("writing preamble: %w", err)
	}

	_, err = rt.writer.Write(creatorJson)
	if err != nil {
		return fmt.Errorf("writing preamble: %w", err)
	}

	_, err = rt.writer.Write([]byte(`,"entries":[` + "\n"))
	if err != nil {
		return fmt.Errorf("writing preamble: %w", err)
	}

	return nil
}

func (rt *RoundTripper) Close() error {
	_, err := rt.writer.Write([]byte("\n]}}"))
	if err != nil {
		return fmt.Errorf("closing har writer: %w", err)
	}

	return nil
}

func (rt *RoundTripper) RoundTrip(request *http.Request) (response *http.Response, err error) {
	entry := &Entry{}
	err = rt.preRoundTrip(request, entry)
	if err != nil {
		return
	}

	trace, clientTrace := newClientTracer()
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), clientTrace))

	response, err = rt.inner.RoundTrip(request)
	if err != nil {
		return
	}

	err = rt.postRoundTrip(response, entry, trace)
	if err != nil {
		return
	}

	err = rt.writeEntry(request, response, entry)
	if err != nil {
		return
	}

	return
}

func (rt *RoundTripper) writeEntry(request *http.Request, response *http.Response, entry *Entry) error {
	entryJson, err := json.Marshal(entry)

	entryJson = rt.opts.Rewrite(request, response, entryJson)
	if entryJson == nil {
		return nil
	}

	rt.mut.Lock()
	defer rt.mut.Unlock()

	if !rt.first {
		_, err = rt.writer.Write([]byte(",\n"))
		if err != nil {
			return fmt.Errorf("writing har entry: %w", err)
		}
	}

	rt.first = false

	_, err = rt.writer.Write(entryJson)
	if err != nil {
		return fmt.Errorf("writing har entry: %w", err)
	}

	return nil
}

func (rt *RoundTripper) preRoundTrip(r *http.Request, entry *Entry) error {
	bodySize := -1
	var postData *PostData
	if r.Body != nil {
		reqBody, err := r.GetBody()
		if err != nil {
			return fmt.Errorf("getting body: %w", err)
		}

		reqBodyBytes, err := io.ReadAll(reqBody)
		if err != nil {
			return fmt.Errorf("reading request body: %w", err)
		}

		bodySize = len(reqBodyBytes)

		mimeType := r.Header.Get("Content-Type")
		postData = &PostData{
			MimeType: mimeType,
			Params:   []*Param{},
			Text:     string(reqBodyBytes),
		}

		mediaType, _, err := mime.ParseMediaType(mimeType)
		if err != nil {
			return fmt.Errorf("parsing request Content-Type: %w", err)
		}

		switch mediaType {
		case "application/x-www-form-urlencoded":
			err = r.ParseForm()
			if err != nil {
				return fmt.Errorf("parsing urlencoded form in request body: %w", err)
			}
			r.Body = io.NopCloser(bytes.NewBuffer(reqBodyBytes))

			for k, v := range r.PostForm {
				for _, s := range v {
					postData.Params = append(postData.Params, &Param{
						Name:  k,
						Value: s,
					})
				}
			}

		case "multipart/form-data":
			err = r.ParseMultipartForm(10 * 1024 * 1024)
			if err != nil {
				return fmt.Errorf("parsing multipart form in request body: %w", err)
			}
			r.Body = io.NopCloser(bytes.NewBuffer(reqBodyBytes))

			for k, v := range r.MultipartForm.Value {
				for _, s := range v {
					postData.Params = append(postData.Params, &Param{
						Name:  k,
						Value: s,
					})
				}
			}
			for k, v := range r.MultipartForm.File {
				for _, s := range v {
					postData.Params = append(postData.Params, &Param{
						Name:        k,
						FileName:    s.Filename,
						ContentType: s.Header.Get("Content-Type"),
					})
				}
			}
		}
	}

	entry.Request = &Request{
		Method:      r.Method,
		URL:         r.URL.String(),
		HTTPVersion: r.Proto,
		Cookies:     toHARCookies(r.Cookies()),
		Headers:     toHARNVP(r.Header),
		QueryString: toHARNVP(r.URL.Query()),
		PostData:    postData,
		HeadersSize: -1, // TODO
		BodySize:    bodySize,
	}

	return nil
}

func (rt *RoundTripper) postRoundTrip(resp *http.Response, entry *Entry, trace *clientTracer) error {
	defer resp.Body.Close()
	respBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	resp.Body = io.NopCloser(bytes.NewBuffer(respBodyBytes))

	mimeType := resp.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil {
		return fmt.Errorf("parsing response Content-Type: %w", err)
	}

	var text string
	var encoding string
	switch {
	case strings.HasPrefix(mediaType, "text/"):
		text = string(respBodyBytes)
	default:
		text = base64.StdEncoding.EncodeToString(respBodyBytes)
		encoding = "base64"
	}

	entry.Response = &Response{
		Status:      resp.StatusCode,
		StatusText:  resp.Status,
		HTTPVersion: resp.Proto,
		Cookies:     toHARCookies(resp.Cookies()),
		Headers:     toHARNVP(resp.Header),
		RedirectURL: resp.Header.Get("Location"),
		HeadersSize: -1,
		BodySize:    resp.ContentLength,
		Content: &Content{
			Size:        resp.ContentLength, // TODO 圧縮されている場合のフォロー
			Compression: 0,
			MimeType:    mimeType,
			Text:        text,
			Encoding:    encoding,
		},
	}

	// TODO: these timings are suspect. the `connect` timing includes the TLS negotiation time (it shouldn't)
	trace.endAt = time.Now()
	entry.StartedDateTime = Time(trace.startAt)
	entry.Time = Duration(trace.endAt.Sub(trace.startAt))
	entry.Timings = &Timings{
		Blocked: Duration(trace.connStart.Sub(trace.startAt)),
		DNS:     -1,
		Connect: -1,
		Send:    Duration(trace.writeRequest.Sub(trace.connObtained)),
		Wait:    Duration(trace.firstResponseByte.Sub(trace.writeRequest)),
		Receive: Duration(trace.endAt.Sub(trace.firstResponseByte)),
		SSL:     -1,
	}
	if !trace.dnsStart.IsZero() {
		entry.Timings.DNS = Duration(trace.dnsEnd.Sub(trace.dnsStart))
	}
	if !trace.connStart.IsZero() {
		entry.Timings.Connect = Duration(trace.connObtained.Sub(trace.connStart))
	}
	if !trace.tlsHandshakeStart.IsZero() {
		entry.Timings.SSL = Duration(trace.tlsHandshakeEnd.Sub(trace.tlsHandshakeStart))
	}

	return nil
}

func toHARCookies(cookies []*http.Cookie) []*Cookie {
	harCookies := make([]*Cookie, 0, len(cookies))

	for _, cookie := range cookies {
		harCookies = append(harCookies, &Cookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Path:     cookie.Path,
			Domain:   cookie.Domain,
			Expires:  Time(cookie.Expires),
			HTTPOnly: cookie.HttpOnly,
			Secure:   cookie.Secure,
		})
	}

	return harCookies
}

func toHARNVP(vs map[string][]string) []*NVP {
	nvps := make([]*NVP, 0, len(vs))

	for k, v := range vs {
		for _, s := range v {
			nvps = append(nvps, &NVP{
				Name:  k,
				Value: s,
			})
		}
	}

	return nvps
}
