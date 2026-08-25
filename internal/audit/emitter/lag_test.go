// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package emitter

import (
	"testing"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
)

// TestSealLagResolution pins unset apart from an explicit zero.
//
// The window declared here is the one the reported seal state is judged
// against, so it has to be the window the sealer runs with. Collapsing "not
// configured" onto "configured as zero" declared the 300 second default for a
// gateway sealing with no lag at all, which describes a gateway that does not
// exist — the seal/submit disagreement ADR 0008 exists to prevent. It is an
// in-package test because the resolution is unexported and the alternative
// route to it needs Postgres.
func TestSealLagResolution(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		opts Options
		want int64
	}{
		{
			name: "unset falls back to the protocol default",
			opts: Options{},
			want: controlplanev1.DefaultSealLagSeconds,
		},
		{
			name: "an explicit zero mirrors a sealer running with no lag",
			opts: Options{SealLagSeconds: SealLag(0)},
			want: 0,
		},
		{
			name: "an explicit non-default window is honoured as given",
			opts: Options{SealLagSeconds: SealLag(30)},
			want: 30,
		},
		{
			name: "a negative window is clamped, as the sealer clamps it",
			opts: Options{SealLagSeconds: SealLag(-5)},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.opts.sealLagSeconds(); got != tc.want {
				t.Errorf("resolved the window to %d, want %d", got, tc.want)
			}
		})
	}
}
