// Copyright 2023 Hanzo AI Inc. All Rights Reserved.
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

import React from "react";
import * as Setting from "../Setting";
import {updateMessage} from "../backend/MessageBackend";

const MessageSuggestions = ({message, sendMessage}) => {
  if (message.author !== "AI" || !message.suggestions || !Array.isArray(message.suggestions)) {
    return null;
  }

  return (
    <div className="flex flex-wrap gap-2">
      {message.suggestions.map((suggestion, index) => {
        let suggestionText = suggestion.text;
        if (suggestionText.trim() === "") {
          return null;
        }

        suggestionText = Setting.formatSuggestion(suggestionText);

        return (
          <button
            key={index}
            className="rounded-lg bg-primary/10 text-primary px-4 py-2 text-sm hover:bg-primary/20 transition-colors text-left leading-relaxed break-words"
            onClick={() => {
              sendMessage(suggestionText, "");
              message.suggestions[index].isHit = true;
              updateMessage(message.owner, message.name, message, true);
            }}
          >
            {suggestionText}
          </button>
        );
      })}
    </div>
  );
};

export default MessageSuggestions;
