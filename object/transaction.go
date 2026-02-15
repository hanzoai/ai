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
	"fmt"
	"strings"

	"github.com/beego/beego/logs"
	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/util"
	"github.com/robfig/cron/v3"
)

var CloudHost = ""

// createTransactionFromMessage creates a transaction object from a message.
// This is a helper function to reduce code duplication.
func createTransactionFromMessage(message *Message) *iamsdk.Transaction {
	transaction := &iamsdk.Transaction{
		Owner:       conf.GetConfigString("iamOrganization"),
		CreatedTime: message.CreatedTime,
		Application: conf.GetConfigString("iamApplication"),
		Domain:      CloudHost,
		Category:    "Hanzo Cloud Chat",
		Type:        message.Chat,
		Subtype:     message.Name,
		Provider:    message.ModelProvider,
		User:        message.User,
		Tag:         "User",
		Amount:      -message.Price,
		Currency:    message.Currency,
		Payment:     "",
		State:       "Paid",
	}

	if util.IsAnonymousUserByUsername(message.User) {
		transaction.Tag = "Organization"
	}

	return transaction
}

// ValidateTransactionForMessage validates a transaction in dry run mode before committing it.
// This checks if the user has sufficient balance without actually creating the transaction.
func ValidateTransactionForMessage(message *Message) error {
	// Only validate transaction if message has a price
	if message.Price <= 0 {
		return nil
	}

	// Create transaction object
	transaction := createTransactionFromMessage(message)

	// Validate transaction via IAM SDK with dry run mode
	_, _, err := iamsdk.AddTransactionWithDryRun(transaction, true)
	if err != nil {
		return fmt.Errorf("failed to validate transaction: %s", err.Error())
	}

	return nil
}

// AddTransactionForMessage creates a transaction in IAM for a message with price,
// sets the message's TransactionId, and if transaction creation fails, updates the message's ErrorText field in the database and returns an error to the caller.
func AddTransactionForMessage(message *Message) error {
	// Only create transaction if message has a price
	if message.Price <= 0 {
		return nil
	}

	// Create transaction object
	transaction := createTransactionFromMessage(message)

	// Add transaction via IAM SDK
	_, transactionName, err := iamsdk.AddTransaction(transaction)
	if err != nil {
		message.ErrorText = fmt.Sprintf("failed to add transaction: %s", err.Error())

		_, errUpdate := UpdateMessage(message.GetId(), message, false)
		if errUpdate != nil {
			return fmt.Errorf("failed to update message: %s", errUpdate.Error())
		}

		return fmt.Errorf("failed to add transaction: %s", err.Error())
	}

	message.TransactionId = util.GetId(transaction.Owner, transactionName)

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
