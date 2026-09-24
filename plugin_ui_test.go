package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCommandCodeDisplayAndBundledLogo(t *testing.T) {
	desc, p := Build(nil)
	if desc.Metadata.Name != "CommandCode" {
		t.Fatalf("unexpected OAuth title: %q", desc.Metadata.Name)
	}
	const prefix = "data:image/svg+xml;base64,"
	if !strings.HasPrefix(desc.Metadata.Logo, prefix) {
		t.Fatal("logo must be a bundled SVG data URI")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(desc.Metadata.Logo, prefix))
	if err != nil || !bytes.Equal(decoded, commandCodeLogo) {
		t.Fatal("bundled logo does not match plugin asset")
	}
	quota, err := p.DescribeQuota(context.Background(), pluginapi.QuotaDescribeRequest{})
	if err != nil || quota.DisplayName != "CommandCode" {
		t.Fatalf("unexpected quota name: %q; %v", quota.DisplayName, err)
	}
}
