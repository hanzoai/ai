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

import React, {useEffect, useState} from "react";
import {ChevronDown, ChevronRight, FileText, Globe} from "lucide-react";
import moment from "moment";
import * as Setting from "../Setting";
import i18next from "i18next";
import {AvatarErrorUrl} from "../Conf";
import {renderText} from "../ChatMessageRender";
import MessageActions from "./MessageActions";
import MessageSuggestions from "./MessageSuggestions";
import MessageEdit from "./MessageEdit";
import {MessageCarrier} from "./MessageCarrier";
import SearchSourcesDrawer from "./SearchSourcesDrawer";
import KnowledgeSourcesDrawer from "./KnowledgeSourcesDrawer";

const MessageItem = ({
  message,
  index,
  isLastMessage,
  account,
  avatar,
  onCopy,
  onRegenerate,
  onLike,
  onToggleRead,
  onEditMessage,
  disableInput,
  isReading,
  isLoadingTTS,
  readingMessage,
  sendMessage,
  hideThinking,
}) => {
  const [avatarSrc, setAvatarSrc] = useState(null);
  const [isRegenerating, setIsRegenerating] = useState(false);
  const [reasonExpanded, setReasonExpanded] = useState(true);
  const [toolsExpanded, setToolsExpanded] = useState(false);
  const [searchDrawerVisible, setSearchDrawerVisible] = useState(false);
  const [knowledgeDrawerVisible, setKnowledgeDrawerVisible] = useState(false);

  const {isEditing,
    setIsHovering,
    renderEditForm,
    renderEditButton,
    handleMouseEnter,
    handleMouseLeave,
  } = MessageEdit({
    message,
    isLastMessage,
    disableInput,
    index,
    onEditMessage,
  });

  useEffect(() => {
    setAvatarSrc(message.author === "AI" ? avatar : Setting.getUserAvatar(message, account));
  }, [message.author, avatar, account, message]);

  const handleAvatarError = () => {
    setAvatarSrc(AvatarErrorUrl);
  };

  const isUserMessage = message.author !== "AI";

  const renderThinkingAnimation = () => (
    <div className="flex items-center gap-2 p-2.5">
      <span className="font-bold text-blue-500 text-sm">{i18next.t("chat:Thinking")}</span>
      <div className="flex gap-1">
        {[0, 1, 2].map((i) => (
          <div
            key={i}
            className="w-1.5 h-1.5 bg-blue-500 rounded-full animate-bounce"
            style={{animationDelay: `${i * 0.16}s`}}
          />
        ))}
      </div>
    </div>
  );

  const renderCollapsible = (title, color, expanded, onToggle, children) => (
    <div className={`mb-4 border-l-[3px] rounded pl-3 py-2`} style={{borderColor: color}}>
      <button
        onClick={() => onToggle(!expanded)}
        className="flex items-center gap-1 text-sm font-bold mb-1"
        style={{color}}
      >
        {expanded ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
        {title}
      </button>
      {expanded && <div className="mt-1">{children}</div>}
    </div>
  );

  const renderMessageContent = () => {
    if (isEditing && !isUserMessage === false) {
      return renderEditForm();
    }

    if (message.errorText !== "") {
      return (
        <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-3">
          <div className="flex items-start gap-2">
            <div className="flex-1">
              <p className="text-sm font-medium text-destructive">{Setting.getRefinedErrorText(message.errorText)}</p>
              <p className="text-xs text-destructive/70 mt-1">{message.errorText}</p>
            </div>
            <button
              onClick={() => {
                setIsRegenerating(true);
                onRegenerate(index);
              }}
              disabled={isRegenerating}
              className="shrink-0 rounded-md bg-destructive px-3 py-1.5 text-xs text-destructive-foreground hover:bg-destructive/90 disabled:opacity-50 transition-colors"
            >
              {isRegenerating ? i18next.t("general:Regenerating...") : i18next.t("general:Regenerate Answer")}
            </button>
          </div>
        </div>
      );
    }

    if (message.text === "" && message.author === "AI" && !message.reasonText) {
      return null;
    }

    if (message.isReasoningPhase && message.author === "AI" && !message.toolCalls && !message.text) {
      return null;
    }

    if ((message.reasonText || message.toolCalls) && message.author === "AI") {
      const themeColor = Setting.getThemeColor();
      const toolColor = (message.reasonText && message.toolCalls) ? "#3b82f6" : themeColor;
      return (
        <div>
          {!hideThinking && message.reasonText && renderCollapsible(
            i18next.t("chat:Reasoning process"),
            themeColor,
            reasonExpanded,
            setReasonExpanded,
            <div className="text-sm">{renderText(message.reasonText)}</div>
          )}
          {message.toolCalls && message.toolCalls.length > 0 && renderCollapsible(
            `${i18next.t("chat:Tool calls")} (${message.toolCalls.length})`,
            toolColor,
            toolsExpanded,
            setToolsExpanded,
            <div className="space-y-2">
              {message.toolCalls.map((toolCall, idx) => (
                <div key={idx} className="pb-2">
                  <div className="font-semibold text-blue-500 text-sm mb-1">{toolCall.name}</div>
                  {toolCall.arguments && (
                    <div className="text-xs font-mono whitespace-pre-wrap break-words mb-1 text-muted-foreground">
                      <strong>Arguments:</strong> {toolCall.arguments}
                    </div>
                  )}
                  {toolCall.content && (
                    <div className="text-xs p-1.5 rounded whitespace-pre-wrap break-words text-muted-foreground">
                      <strong>Result:</strong> {toolCall.content}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
          <div>{message.html || renderText(message.text)}</div>
        </div>
      );
    }

    if (isLastMessage && message.author === "AI" && message.TokenCount === 0) {
      const mssageCarrier = new MessageCarrier(false);
      return renderText(mssageCarrier.parseAnswerWithCarriers(message.text).finalAnswer);
    }

    return message.html;
  };

  const renderReasoningBubble = () => {
    if (message.isReasoningPhase && message.author === "AI" && message.reasonText) {
      const themeColor = Setting.getThemeColor();
      return (
        <div className="mb-2">
          <div className="flex gap-3">
            <img
              src={avatarSrc}
              alt="AI"
              className="w-8 h-8 rounded-full shrink-0"
              onError={handleAvatarError}
            />
            <div className="rounded-2xl bg-card border border-border px-4 py-3 max-w-[80%]">
              {hideThinking ? renderThinkingAnimation() : renderCollapsible(
                i18next.t("chat:Reasoning process"),
                themeColor,
                reasonExpanded,
                setReasonExpanded,
                <div className="text-sm">{renderText(message.reasonText)}</div>
              )}
            </div>
          </div>
        </div>
      );
    }
    return null;
  };

  const renderMessageBubble = () => {
    if (message.isReasoningPhase && message.author === "AI") {
      return null;
    }

    const isLoading = message.text === "" && message.author === "AI" && !message.reasonText && !message.errorText;

    return (
      <div className={`flex ${isUserMessage ? "justify-end" : "justify-start"} items-start gap-3 relative`}>
        {isUserMessage && renderEditButton()}

        {!isUserMessage && (
          <img
            src={avatarSrc}
            alt="avatar"
            className="w-8 h-8 rounded-full shrink-0"
            onError={handleAvatarError}
          />
        )}

        <div className={`max-w-[80%] ${isUserMessage ? "order-first" : ""}`}>
          <div className={`rounded-2xl px-4 py-3 ${
            isUserMessage
              ? "bg-primary text-primary-foreground"
              : "bg-card border border-border text-card-foreground"
          } ${isEditing && !isUserMessage ? "min-w-[300px]" : ""}`}>
            {isLoading ? (
              <div className="flex gap-1 py-1">
                {[0, 1, 2].map((i) => (
                  <div key={i} className="w-2 h-2 bg-muted-foreground rounded-full animate-bounce" style={{animationDelay: `${i * 0.16}s`}} />
                ))}
              </div>
            ) : (
              renderMessageContent()
            )}
          </div>

          {/* Footer: Actions + Sources + Suggestions */}
          <div className="mt-1.5 space-y-2">
            {!isEditing && message.author === "AI" && (disableInput === false || index !== isLastMessage) && (
              <div className="flex items-center gap-2 flex-wrap">
                <MessageActions
                  message={message}
                  isLastMessage={isLastMessage}
                  index={index}
                  onCopy={onCopy}
                  onRegenerate={onRegenerate}
                  onLike={onLike}
                  onToggleRead={onToggleRead}
                  onEdit={() => setIsHovering(true)}
                  isReading={isReading}
                  isLoadingTTS={isLoadingTTS}
                  readingMessage={readingMessage}
                  account={account}
                  setIsRegenerating={setIsRegenerating}
                  isRegenerating={isRegenerating}
                />
                {message.searchResults?.length > 0 && (
                  <button
                    onClick={() => setSearchDrawerVisible(true)}
                    className="inline-flex items-center gap-1 text-xs text-primary hover:text-primary/80 transition-colors"
                  >
                    <Globe className="w-3 h-3" />
                    {message.searchResults.length} {i18next.t("chat:Web sources")}
                  </button>
                )}
                {message.vectorScores?.length > 0 && (
                  <button
                    onClick={() => setKnowledgeDrawerVisible(true)}
                    className="inline-flex items-center gap-1 text-xs text-primary hover:text-primary/80 transition-colors"
                  >
                    <FileText className="w-3 h-3" />
                    {message.vectorScores.length} {i18next.t("chat:Knowledge sources")}
                  </button>
                )}
              </div>
            )}
            {message.author === "AI" && isLastMessage && (
              <MessageSuggestions message={message} sendMessage={sendMessage} />
            )}
          </div>
        </div>

        {isUserMessage && (
          <img
            src={avatarSrc}
            alt="avatar"
            className="w-8 h-8 rounded-full shrink-0"
            onError={handleAvatarError}
          />
        )}
      </div>
    );
  };

  return (
    <>
      <div
        className={`max-w-[90%] relative mb-4 ${isUserMessage ? "ml-auto" : "mr-auto"}`}
        onMouseEnter={handleMouseEnter}
        onMouseLeave={handleMouseLeave}
      >
        <div className={`text-xs text-muted-foreground mb-2 px-3 ${isUserMessage ? "text-right" : "text-left"}`}>
          {moment(message.createdTime).format("YYYY/M/D HH:mm:ss")}
        </div>

        {renderReasoningBubble()}
        {renderMessageBubble()}
      </div>

      <SearchSourcesDrawer
        visible={searchDrawerVisible}
        onClose={() => setSearchDrawerVisible(false)}
        searchResults={message.searchResults}
      />

      <KnowledgeSourcesDrawer
        visible={knowledgeDrawerVisible}
        onClose={() => setKnowledgeDrawerVisible(false)}
        vectorScores={message.vectorScores}
        account={account}
      />
    </>
  );
};

export default MessageItem;
