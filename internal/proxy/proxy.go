package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Forwarder struct {
	client *http.Client
}

func NewForwarder() *Forwarder {
	return &Forwarder{
		client: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return errors.New("redirects are not allowed")
			},
		},
	}
}

func (f *Forwarder) Forward(event GitHubPushEvent, docoCDURL string, secret []byte) (int, error) {
	body, err := event.Marshal()
	if err != nil {
		return 0, fmt.Errorf("failed to marshal event: %w", err)
	}

	url := strings.TrimRight(docoCDURL, "/") + "/v1/webhook"

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", ComputeSignature(body, secret))

	resp, err := f.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode, nil
}
