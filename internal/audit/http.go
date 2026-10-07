package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/credentials"
)

// HTTPSink forwards audit events to an external collector. A token file is
// reread for every event so operators can rotate the collector credential.
type HTTPSink struct {
	mu       sync.Mutex
	endpoint string
	token    string
	client   *http.Client
	lastErr  error
}

func NewHTTPSink(endpoint, tokenFile string) (*HTTPSink, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("audit endpoint is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return nil, errors.New("audit endpoint must be an HTTP(S) URL without embedded credentials")
	}
	return &HTTPSink{endpoint: endpoint, token: tokenFile, client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (s *HTTPSink) Record(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	body, err := json.Marshal(event)
	if err == nil {
		request, requestErr := http.NewRequest(http.MethodPost, s.endpoint, bytes.NewReader(body))
		if requestErr != nil {
			err = requestErr
		} else {
			request.Header.Set("Content-Type", "application/json")
			if s.token != "" {
				var token string
				token, err = credentials.Resolve("", s.token)
				if err == nil {
					request.Header.Set("Authorization", "Bearer "+token)
				}
			}
			if err == nil {
				var response *http.Response
				response, err = s.client.Do(request)
				if err == nil {
					if response.StatusCode < 200 || response.StatusCode >= 300 {
						err = errors.New("audit collector rejected event")
					}
					_ = response.Body.Close()
				}
			}
		}
	}
	s.lastErr = err
}

func (s *HTTPSink) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

type MultiSink []Sink

func (s MultiSink) Record(event Event) {
	for _, sink := range s {
		if sink != nil {
			sink.Record(event)
		}
	}
}

func (s MultiSink) LastError() error {
	var failures []error
	for _, sink := range s {
		if reporter, ok := sink.(interface{ LastError() error }); ok {
			failures = append(failures, reporter.LastError())
		}
	}
	return errors.Join(failures...)
}
