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
import MessageItem from "./MessageItem";
import * as Setting from "../Setting";

const MessageList = React.forwardRef(({
  messages,
  account,
  store,
  onRegenerate,
  onMessageLike,
  onCopyMessage,
  onToggleRead,
  onEditMessage,
  previewMode,
  hideInput,
  disableInput,
  isReading,
  isLoadingTTS,
  readingMessage,
  sendMessage,
  files,
  hideThinking,
}, ref) => {
  const avatarSrc = store?.avatar || Setting.getDefaultAiAvatar();
  const filteredMessages = messages.filter(message => message.isHidden === false);

  return (
    <div
      ref={ref}
      className={`flex-1 p-6 ${previewMode ? "overflow-hidden relative" : "overflow-auto absolute inset-0"}`}
      style={{
        bottom: hideInput ? "0px" : files?.length > 0 ? "150px" : "100px",
        scrollBehavior: "smooth",
        paddingBottom: hideInput ? "0px" : "40px",
      }}
    >
      {filteredMessages.length === 0 ? (
        <div className="text-center text-muted-foreground py-8">&nbsp;</div>
      ) : (
        <div className="space-y-2">
          {filteredMessages.map((message, index) => (
            <MessageItem
              key={message.name || index}
              message={message}
              index={index}
              isLastMessage={index === filteredMessages.length - 1}
              account={account}
              avatar={avatarSrc}
              onCopy={onCopyMessage}
              onRegenerate={onRegenerate}
              onLike={onMessageLike}
              onToggleRead={onToggleRead}
              onEditMessage={onEditMessage}
              disableInput={disableInput}
              isReading={isReading}
              isLoadingTTS={isLoadingTTS}
              readingMessage={readingMessage}
              sendMessage={sendMessage}
              hideThinking={hideThinking}
            />
          ))}
        </div>
      )}
    </div>
  );
});

export default MessageList;
