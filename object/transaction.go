// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package object

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/beego/beego/logs"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/util"
	"github.com/robfig/cron/v3"
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

	endpoint, token, client := commerceClient()
	if endpoint == "" {
		return fmt.Errorf("commerceEndpoint is not configured")
	}

	// Build the user identifier: owner/name format expected by Commerce
	userId := message.User
	if message.Owner != "" && !strings.Contains(userId, "/") {
		userId = message.Owner + "/" + userId
	}

	// Convert price (dollars float64) to cents for comparison
	priceCents := int64(math.Round(message.Price * 100))

	cur := strings.ToLower(message.Currency)
	if cur == "" {
		cur = "usd"
	}

	// Query Commerce for balance
	url := fmt.Sprintf("%s/api/v1/billing/balance?user=%s&currency=%s",
		endpoint, userId, cur)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to build balance request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

	endpoint, token, client := commerceClient()
	if endpoint == "" {
		return fmt.Errorf("commerceEndpoint is not configured")
	}

	// Build the user identifier
	userId := message.User
	if message.Owner != "" && !strings.Contains(userId, "/") {
		userId = message.Owner + "/" + userId
	}

	// Convert price (dollars float64) to cents
	amountCents := int64(math.Round(message.Price * 100))
	if amountCents <= 0 {
		return nil
	}

	cur := strings.ToLower(message.Currency)
	if cur == "" {
		cur = "usd"
	}

	payload := map[string]interface{}{
		"user":     userId,
		"currency": cur,
		"amount":   amountCents,
		"model":    message.ModelProvider,
		"provider": message.ModelProvider,
		"requestId": util.GetRandomName(),
		"premium":  true,
		"stream":   false,
		"status":   "success",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal usage payload: %w", err)
	}

	url := endpoint + "/api/v1/billing/usage"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build usage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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
		logs.Warning("failed to decode Commerce response: %s", err.Error())
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
		logs.Error("retryFailedTransactionNoError() error: %s", err.Error())
	}
}

func InitMessageTransactionRetry() {
	cronJob := cron.New()
	schedule := "@every 5m"
	_, err := cronJob.AddFunc(schedule, retryFailedTransactionNoError)
	if err != nil {
		panic(err)
	}

	cronJob.Start()
}
