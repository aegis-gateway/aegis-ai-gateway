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

package types

type Classification string

const (
	ClassPublic       Classification = "PUBLIC"
	ClassInternal     Classification = "INTERNAL"
	ClassConfidential Classification = "CONFIDENTIAL"
	ClassRestricted   Classification = "RESTRICTED"
)

// ClassificationLevel returns a numeric level for comparison.
// Higher values mean more restricted.
func (c Classification) Level() int {
	switch c {
	case ClassPublic:
		return 0
	case ClassInternal:
		return 1
	case ClassConfidential:
		return 2
	case ClassRestricted:
		return 3
	default:
		return -1
	}
}

// Allows returns true if this classification level permits access to data at the given level.
func (c Classification) Allows(data Classification) bool {
	return c.Level() >= data.Level()
}

func ParseClassification(s string) (Classification, bool) {
	switch Classification(s) {
	case ClassPublic, ClassInternal, ClassConfidential, ClassRestricted:
		return Classification(s), true
	default:
		return "", false
	}
}
