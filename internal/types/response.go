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

type AegisResponse struct {
	RequestID        string        `json:"request_id"`
	Model            string        `json:"model"`
	Provider         string        `json:"provider"`
	Choices          []Choice      `json:"choices"`
	Usage            Usage         `json:"usage"`
	EstimatedCostUSD float64       `json:"estimated_cost_usd"`
	FilterActions    FilterSummary `json:"filter_actions"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type FilterSummary struct {
	PIIInbound  FilterAction `json:"pii_inbound"`
	PIIOutbound FilterAction `json:"pii_outbound"`
	Secrets     FilterAction `json:"secrets"`
	Injection   FilterAction `json:"injection"`
	Policy      FilterAction `json:"policy"`
}

type FilterAction struct {
	Action     string  `json:"action"`
	Detections int     `json:"detections,omitempty"`
	Score      float64 `json:"score,omitempty"`
}
