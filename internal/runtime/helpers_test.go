// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import "testing"

func TestInstanceID(t *testing.T) {
	t.Setenv(envCID, "")
	if got := instanceID(); got != "0" {
		t.Fatalf("无 CID 时应退化为 0，实得 %s", got)
	}
	t.Setenv(envCID, "abc123")
	if got := instanceID(); got != "abc123" {
		t.Fatalf("应读取 CID，实得 %s", got)
	}
}
