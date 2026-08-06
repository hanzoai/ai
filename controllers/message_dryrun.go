// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"fmt"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// dryRunWriter is a dummy writer that implements both io.Writer and http.Flusher
// It discards all writes and is used for dry run queries that need a Flusher
type dryRunWriter struct{}

func (w *dryRunWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func (w *dryRunWriter) Flush() {}

// shouldPerformDryRun determines if a dry run estimation should be performed
// before generating the actual AI answer. Dry run is skipped for:
// - Dummy providers (no real AI calls)
// - Reason models (they have different execution paths)
// - Queries with agent clients (agent-based workflows)
func shouldPerformDryRun(providerType string, modelSubType string, hasAgentClients bool) bool {
	return providerType != "Dummy" && !isReasonModel(modelSubType) && !hasAgentClients
}

// gateBalance refuses an AI generation BEFORE it spends upstream when the payer
// cannot cover what that generation is estimated to cost. It is the ONE pre-flight
// money gate every answer surface runs, so no surface can generate for free.
//
// The estimate is the provider's OWN dry run over the same question, history and
// prompt the real call will carry, so the number gated on is the number billed —
// never a rate-table guess keyed on a provider name. A provider with no dry run
// (Dummy, reason models) has nothing to estimate and is not gated.
//
// owner/user name the payer, and they are the SAME pair AddTransactionForMessage
// debits: a call this gate admits depletes the account this gate read.
func gateBalance(
	owner string,
	user string,
	question string,
	history []*model.RawMessage,
	prompt string,
	modelProvider *object.Provider,
	modelProviderObj model.ModelProvider,
	acceptLanguage string,
) error {
	if !shouldPerformDryRun(modelProvider.Type, modelProvider.SubType, false) {
		return nil
	}

	// The dry run marker makes the provider estimate the call instead of running it.
	// dryRunWriter is an io.Writer AND an http.Flusher — some providers require both
	// even when they answer nothing.
	estimate, err := modelProviderObj.QueryText(model.DryRunPrefix+question, &dryRunWriter{}, history, prompt, nil, nil, acceptLanguage)
	if err != nil {
		return fmt.Errorf("failed to estimate token count: %s", err.Error())
	}

	return object.ValidateTransactionForMessage(&object.Message{
		Owner:         owner,
		User:          user,
		ModelProvider: modelProvider.Name,
		Price:         estimate.TotalPrice,
		Currency:      estimate.Currency,
	})
}

// validateTransactionBeforeAIGeneration runs gateBalance for a chat turn, whose
// history is the chat's own recent messages and whose prompt is the store's. It
// reports refusal on the message's event stream rather than as a bare error.
func validateTransactionBeforeAIGeneration(
	message *object.Message,
	chat *object.Chat,
	store *object.Store,
	question string,
	modelProvider *object.Provider,
	modelProviderObj model.ModelProvider,
	acceptLanguage string,
	responseErrorFunc func(*object.Message, string),
) error {
	if !shouldPerformDryRun(modelProvider.Type, modelProvider.SubType, false) {
		return nil
	}

	history, err := object.GetRecentRawMessages(chat.Name, message.CreatedTime, store.MemoryLimit)
	if err != nil {
		responseErrorFunc(message, err.Error())
		return err
	}

	err = gateBalance(message.Owner, message.User, question, history, store.Prompt, modelProvider, modelProviderObj, acceptLanguage)
	if err != nil {
		responseErrorFunc(message, err.Error())
		return err
	}

	return nil
}
