package runner

import (
	"io"
	"net/http"
	"testing"
)

func TestExecuteUsesNilBodyForEmptyPayload(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationGET)
	var gotBodyNil bool
	var gotGetBodyNil bool
	var gotContentLength int64
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		gotBodyNil = httpReq.Body == nil
		gotGetBodyNil = httpReq.GetBody == nil
		gotContentLength = httpReq.ContentLength
		return optimizedHTTPResponse(httpReq, http.StatusNoContent, http.Header{}, http.NoBody, 0), nil
	})

	if _, err := runner.Execute(&req); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !gotBodyNil || !gotGetBodyNil || gotContentLength != 0 {
		t.Fatalf("empty payload request = Body nil %t, GetBody nil %t, ContentLength %d; want true, true, 0", gotBodyNil, gotGetBodyNil, gotContentLength)
	}
}

func TestExecutePreservesNonEmptyPayload(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationGET)
	req.Data = []byte("payload")
	var gotBody []byte
	var gotContentLength int64
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		var err error
		gotBody, err = io.ReadAll(httpReq.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		gotContentLength = httpReq.ContentLength
		return optimizedHTTPResponse(httpReq, http.StatusNoContent, http.Header{}, http.NoBody, 0), nil
	})

	if _, err := runner.Execute(&req); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(gotBody) != "payload" || gotContentLength != int64(len(req.Data)) {
		t.Fatalf("non-empty payload request = body %q, ContentLength %d; want %q, %d", gotBody, gotContentLength, req.Data, len(req.Data))
	}
}

func TestResponseOptimizationHEADUsesNilBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	report := responseOptimizationReport{mode: responseOptimizationHEAD}
	if !report.apply(req) {
		t.Fatal("HEAD optimization was not applied")
	}
	if req.Body != nil {
		t.Fatalf("optimized HEAD Body = %T; want nil to avoid quic-go's body-copy path", req.Body)
	}
}
