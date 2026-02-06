package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	client "github.com/CloudNativeWorks/elchi-proto/client"
)

var defaultHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

func Do(ctx context.Context, req *client.RequestEnvoyAdmin) (*client.ResponseEnvoyAdmin, error) {
	u := fmt.Sprintf("http://127.0.0.1:%d%s", req.Port, req.Path)

	if len(req.Queries) > 0 {
		params := url.Values{}
		for k, v := range req.Queries {
			params.Set(k, v)
		}
		u += "?" + params.Encode()
	}

	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = bytes.NewReader([]byte(req.Body))
	} else {
		bodyReader = nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method.String(), u, bodyReader)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := defaultHTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	headers := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	return &client.ResponseEnvoyAdmin{
		StatusCode: int32(resp.StatusCode),
		Body:       string(respBody),
		Headers:    headers,
	}, nil
}
