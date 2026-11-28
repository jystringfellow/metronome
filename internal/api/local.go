package api

import (
	"net/http"
	"net/http/httptest"
)

type localTransport struct{ handler http.Handler }

func (t localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

func LocalHTTP(handler http.Handler) *http.Client {
	return &http.Client{Transport: localTransport{handler: handler}}
}
