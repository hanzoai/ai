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
import {Copy, Loader2, Pause, Play, RefreshCw, ThumbsDown, ThumbsUp} from "lucide-react";
import i18next from "i18next";

const ActionButton = ({icon: Icon, title, onClick, disabled, filled}) => (
  <button
    title={title}
    onClick={onClick}
    disabled={disabled}
    className={`p-1.5 rounded-md transition-colors ${
      disabled
        ? "text-muted-foreground/40 cursor-not-allowed"
        : "text-muted-foreground hover:text-foreground hover:bg-secondary"
    } ${filled ? "text-foreground" : ""}`}
  >
    <Icon className="w-4 h-4" fill={filled ? "currentColor" : "none"} />
  </button>
);

const MessageActions = ({
  message,
  isLastMessage,
  index,
  onCopy,
  onRegenerate,
  onLike,
  onToggleRead,
  isReading,
  isLoadingTTS,
  readingMessage,
  account,
  setIsRegenerating,
  isRegenerating,
}) => {
  const isCurrentMessageBeingRead = readingMessage === message.name;
  const isCurrentMessageBeingLoaded = isLoadingTTS && isCurrentMessageBeingRead;

  const getTtsIcon = () => {
    if (isCurrentMessageBeingLoaded) {return Loader2;}
    if (isCurrentMessageBeingRead && isReading) {return Pause;}
    return Play;
  };

  const getTtsTooltip = () => {
    if (isCurrentMessageBeingLoaded) {return i18next.t("general:Loading...");}
    if (isCurrentMessageBeingRead && isReading) {return i18next.t("general:Pause");}
    if (isCurrentMessageBeingRead && !isReading) {return i18next.t("general:Resume");}
    return i18next.t("chat:Read it out");
  };

  return (
    <div className="flex items-center gap-0.5 opacity-80">
      <ActionButton
        icon={Copy}
        title={i18next.t("general:Copy")}
        onClick={() => onCopy(message.text)}
      />

      <ActionButton
        icon={ThumbsUp}
        title={i18next.t("general:Like")}
        onClick={() => onLike(message, "like")}
        filled={message.likeUsers?.includes(account.name)}
      />

      <ActionButton
        icon={ThumbsDown}
        title={i18next.t("general:Dislike")}
        onClick={() => onLike(message, "dislike")}
        filled={message.dislikeUsers?.includes(account.name)}
      />

      <ActionButton
        icon={getTtsIcon()}
        title={getTtsTooltip()}
        onClick={() => onToggleRead(message)}
        disabled={isCurrentMessageBeingLoaded}
      />

      {isLastMessage && (
        <ActionButton
          icon={RefreshCw}
          title={i18next.t("general:Regenerate Answer")}
          onClick={() => {
            setIsRegenerating(true);
            onRegenerate(index);
          }}
          disabled={isRegenerating}
        />
      )}
    </div>
  );
};

export default MessageActions;
