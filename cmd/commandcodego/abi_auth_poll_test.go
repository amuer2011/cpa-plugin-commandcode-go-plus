package main

import "testing"

func TestDecodeAuthLoginPollRequestRestoresHostClient(t *testing.T) {
	req, err := decodeAuthLoginPollRequest([]byte(`{"provider":"commandcode-go","state":"test-state","host_callback_id":"cb-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	client, ok := req.HTTPClient.(abiHostHTTPClient)
	if !ok || client.callbackID != "cb-test" || req.State != "test-state" {
		t.Fatal("poll request lost the host HTTP callback context")
	}
}

func TestDecodeAuthLoginPollRequestWithoutCallbackDoesNotInventClient(t *testing.T) {
	req, err := decodeAuthLoginPollRequest([]byte(`{"state":"test-state"}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.HTTPClient != nil {
		t.Fatal("poll request unexpectedly created host HTTP client")
	}
}
