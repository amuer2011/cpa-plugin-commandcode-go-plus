package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestManagementCapabilityAndRoutesSurviveABI(t *testing.T) {
	raw, err := handleRegister([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var registration struct {
		Result abiRegistration `json:"result"`
	}
	if json.Unmarshal(raw, &registration) != nil || !registration.Result.Capabilities.ManagementAPI {
		t.Fatalf("missing management capability: %s", raw)
	}
	raw, err = handleABIMethod(context.Background(), pluginabi.MethodManagementRegister, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var routes struct {
		Result struct{ Resources []struct{ Path string } } `json:"result"`
	}
	if json.Unmarshal(raw, &routes) != nil || len(routes.Result.Resources) != 3 {
		t.Fatalf("resource routes missing from ABI: %s", raw)
	}
	raw, err = handleABIMethod(context.Background(), pluginabi.MethodManagementHandle, []byte(`{"Method":"GET","Path":"/v0/resource/plugins/commandcode-go/oauth/check"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct{ StatusCode int } `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Result.StatusCode != 410 {
		t.Fatalf("resource dispatch failed: %s", raw)
	}
}
