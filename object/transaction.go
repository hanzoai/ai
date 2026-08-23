// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/util"
)

var CloudHost = ""

// commerceClient returns an HTTP client and the Commerce billing endpoint URL.
// Returns ("", nil) if Commerce is not configured.
func commerceClient() (string, string, *http.Client) {
	endpoint := conf.GetConfigString("commerceEndpoint")
	if endpoint == "" {
		return "", "", nil
	}
	endpoint = strings.TrimRight(endpoint, "/")
	token := conf.GetConfigString("commerceToken")
	return endpoint, token, &http.Client{Timeout: 10 * time.Second}
}

// ValidateTransactionForMessage validates that the user has sufficient balance
// before committing an expensive AI generation. Checks balance via Commerce.
func ValidateTransactionForMessage(message *Message) error {
	// Only validate if message has a price
	if message.Price <= 0 {
		return nil
	}
	// Build the user identifier: owner/name format expected by Commerce
	userId := message.User
	if message.Owner != "" && !strings.Contains(userId, "/") {
		userId = message.Owner + "/" + userId
	}
	// A call that costs anything needs at least a cent behind it, so this rounds UP.
	// To nearest, everything under half a cent priced at zero — and zero is covered
	// by an empty wallet, since the comparison below is "available < required". The
	// cheapest calls were therefore free, and free is repeatable.
	priceCents := int64(math.Ceil(message.Price * 100))
	cur := strings.ToLower(message.Currency)
	if cur == "" {
		cur = "usd"
	}
	// Native path: read the wallet balance DIRECTLY from the host's in-process
	// finance ledger (cloud), no HTTP.
	if balanceReader != nil {
		avail, err := balanceReader(context.Background(), userId, message.Owner, cur)
		if err != nil {
			return fmt.Errorf("failed to check balance: %w", err)
		}
		if avail < priceCents {
			return fmt.Errorf("insufficient balance: available %d cents, required %d cents", avail, priceCents)
		}
		return nil
	}
	endpoint, token, client := commerceClient()
	if endpoint == "" {
		return fmt.Errorf("commerceEndpoint is not configured")
	}
	// Query Commerce for balance. API mounted at root in prod
	// All commerce endpoints live under /v1/.
	url := fmt.Sprintf("%s/v1/billing/balance?user=%s&currency=%s",
		endpoint, userId, cur)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to build balance request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Scope the service-token call to this message's org namespace, so the
	// balance read hits the SAME per-org ledger the usage debit writes.
	if message.Owner != "" {
		req.Header.Set("X-Org-Id", message.Owner)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to check balance: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Commerce balance check returned status %d", resp.StatusCode)
	}
	var result struct {
		Available int64 `json:"available"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse balance response: %w", err)
	}
	if result.Available < priceCents {
		return fmt.Errorf("insufficient balance: available %d cents, required %d cents", result.Available, priceCents)
	}
	return nil
}

// AddTransactionForMessage creates a withdraw transaction in Commerce for a message
// with price, sets the message's TransactionId, and if transaction creation fails,
// updates the message's ErrorText field in the database and returns an error.
func AddTransactionForMessage(message *Message) error {
	// Only create transaction if message has a price
	if message.Price <= 0 {
		return nil
	}
	// Build the user identifier
	userId := message.User
	if message.Owner != "" && !strings.Contains(userId, "/") {
		userId = message.Owner + "/" + userId
	}
	cur := strings.ToLower(message.Currency)
	if cur == "" {
		cur = "usd"
	}
	// Native path: debit the usage DIRECTLY to the host's in-process finance ledger
	// (cloud), no HTTP. The price (dollars) is carried EXACTLY as a decimal-USD string
	// to nano precision — never floored to cents — so a sub-cent message bills precisely.
	//
	// This charge is NOT idempotent, on the message id or on anything else. The
	// ledger mints its own entry id per debit and never reads the RequestID we send,
	// so calling this twice for one completion charges twice. Whoever calls it owes
	// the once — see the claim GetMessageAnswer takes on the message before it
	// generates. That is also why the failed-transaction retry below is HTTP-only:
	// re-driving a native debit whose first attempt may have landed would double
	// charge, with nothing downstream to collapse the pair.
	if usageRecorder != nil {
		return usageRecorder(context.Background(), UsageEvent{
			Subject:   userId,
			Namespace: message.Owner,
			USD:       strconv.FormatFloat(message.Price, 'f', 9, 64),
			Currency:  cur,
			Model:     message.ModelProvider,
			Provider:  message.ModelProvider,
			RequestID: message.GetId(),
		})
	}
	// HTTP fallback (standalone ai): Commerce speaks cents.
	amountCents := int64(math.Round(message.Price * 100))
	if amountCents <= 0 {
		return nil
	}
	endpoint, token, client := commerceClient()
	if endpoint == "" {
		return fmt.Errorf("commerceEndpoint is not configured")
	}
	payload := map[string]interface{}{
		"user":      userId,
		"currency":  cur,
		"amount":    amountCents,
		"model":     message.ModelProvider,
		"provider":  message.ModelProvider,
		"requestId": util.GetRandomName(),
		"premium":   true,
		"stream":    false,
		"status":    "success",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal usage payload: %w", err)
	}
	url := endpoint + "/v1/billing/usage"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build usage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Debit the SAME per-org ledger the balance gate reads: the service-token
	// namespace is the message's org, never commerce's "hanzo" default.
	if message.Owner != "" {
		req.Header.Set("X-Org-Id", message.Owner)
	}
	resp, err := client.Do(req)
	if err != nil {
		message.ErrorText = fmt.Sprintf("failed to add transaction: %s", err.Error())
		_, errUpdate := UpdateMessage(message.GetId(), message, false)
		if errUpdate != nil {
			return fmt.Errorf("failed to update message: %s", errUpdate.Error())
		}
		return fmt.Errorf("failed to add transaction: %s", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("Commerce returned status %d: %s", resp.StatusCode, string(bodyBytes))
		message.ErrorText = fmt.Sprintf("failed to add transaction: %s", errMsg)
		_, errUpdate := UpdateMessage(message.GetId(), message, false)
		if errUpdate != nil {
			return fmt.Errorf("failed to update message: %s", errUpdate.Error())
		}
		return fmt.Errorf("failed to add transaction: %s", errMsg)
	}
	var result struct {
		TransactionId string `json:"transactionId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Warning("failed to decode Commerce response: %s", err.Error())
	} else if result.TransactionId != "" {
		message.TransactionId = result.TransactionId
	}
	return nil
}

func retryFailedTransaction() error {
	messages, err := GetGlobalFailMessages()
	if err != nil {
		return err
	}
	for _, message := range messages {
		if strings.HasPrefix(message.ErrorText, "failed to add transaction") {
			err = AddTransactionForMessage(message)
			if err != nil {
				return err
			}
			message.ErrorText = ""
			_, err = UpdateMessage(message.GetId(), message, false)
			if err != nil {
				return fmt.Errorf("failed to update message: %s", err.Error())
			}
		}
	}
	return nil
}

func retryFailedTransactionNoError() {
	err := retryFailedTransaction()
	if err != nil {
		log.Error("retryFailedTransactionNoError() error: %s", err.Error())
	}
}

func InitMessageTransactionRetry() {
	cronJob := newCron()
	schedule := "@every 5m"
	_, err := cronJob.AddFunc(schedule, retryFailedTransactionNoError)
	if err != nil {
		panic(err)
	}
	cronJob.Start()
}
