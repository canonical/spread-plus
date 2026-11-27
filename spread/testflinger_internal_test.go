package spread

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetIPForReservedDeviceUsesDeviceInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/oauth2/token":
			_, _ = w.Write([]byte(`{"access_token":"test-token"}`))
		case "/v1/result/job-id":
			err := json.NewEncoder(w).Encode(TestFlingerResultResponse{
				JobState: RESERVE,
				DeviceInfo: TestFlingerDeviceInfo{
					DeviceIP: "10.1.2.3",
				},
			})
			if err != nil {
				t.Errorf("cannot write result response: %v", err)
			}
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("TF_ENDPOINT", server.URL)
	t.Setenv("TF_API_VERSION", "v1")
	provider := &TestFlingerProvider{client: server.Client()}
	job := &TestFlingerJob{p: provider, d: TestFlingerJobData{JobId: "job-id"}}

	ip, err := provider.getIpForReservedDevice(job)
	if err != nil {
		t.Fatalf("getIpForReservedDevice returned error: %v", err)
	}
	if ip != "10.1.2.3" {
		t.Fatalf("getIpForReservedDevice returned %q, want %q", ip, "10.1.2.3")
	}
}

func TestGetJobResultRetriesAndFetchesNewToken(t *testing.T) {
	var tokenRequests, resultRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/oauth2/token":
			tokenRequests++
			_, _ = w.Write([]byte(`{"access_token":"test-token"}`))
		case "/v1/result/job-id":
			resultRequests++
			if resultRequests == 1 {
				_, _ = w.Write([]byte("not-json"))
				return
			}
			_ = json.NewEncoder(w).Encode(TestFlingerResultResponse{
				JobState: RESERVE,
				DeviceInfo: TestFlingerDeviceInfo{
					DeviceIP: "10.1.2.3",
				},
			})
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("TF_ENDPOINT", server.URL)
	t.Setenv("TF_API_VERSION", "v1")
	provider := &TestFlingerProvider{client: server.Client()}

	var result TestFlingerResultResponse
	if err := provider.getJobResult("job-id", &result); err != nil {
		t.Fatalf("getJobResult returned error: %v", err)
	}
	if result.DeviceInfo.DeviceIP != "10.1.2.3" {
		t.Fatalf("getJobResult returned device IP %q, want %q", result.DeviceInfo.DeviceIP, "10.1.2.3")
	}
	if resultRequests != 2 || tokenRequests != 2 {
		t.Fatalf("requests: result=%d token=%d, want both 2", resultRequests, tokenRequests)
	}
}
