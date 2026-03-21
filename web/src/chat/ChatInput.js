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

import React, {useRef} from "react";
import {Globe, Mic, MicOff, Send, Square, X} from "lucide-react";
import ChatFileInput from "./ChatFileInput";
import UploadFileArea from "./UploadFileArea";
import ChatInputMenu from "./ChatInputMenu";
import i18next from "i18next";

const ChatInput = ({
  value,
  store,
  chat,
  files,
  onFileChange,
  onChange,
  onSend,
  loading,
  disableInput,
  messageError,
  onCancelMessage,
  onVoiceInputStart,
  onVoiceInputEnd,
  isVoiceInput,
  webSearchEnabled,
  onWebSearchChange,
}) => {
  const textareaRef = useRef(null);
  const sendButtonDisabled = messageError || (value === "" && files.length === 0) || disableInput;

  async function handleInputChange(file) {
    const reader = new FileReader();
    if (file.type.startsWith("image/")) {
      reader.onload = (e) => {
        const img = new Image();
        img.onload = () => {
          const originalWidth = img.width;
          const originalHeight = img.height;
          const inputMaxWidth = 70;
          const chatMaxWidth = 600;
          let Ratio = 1;
          if (originalWidth > inputMaxWidth) {
            Ratio = inputMaxWidth / originalWidth;
          }
          if (originalWidth > chatMaxWidth) {
            Ratio = chatMaxWidth / originalWidth;
          }
          const chatScaledWidth = Math.round(originalWidth * Ratio);
          const chatScaledHeight = Math.round(originalHeight * Ratio);
          const value = `<img src="${img.src}" alt="${img.alt}" width="${chatScaledWidth}" height="${chatScaledHeight}">`;
          updateFileList(file, img.src, value);
        };
        img.src = e.target.result;
      };
    } else {
      reader.onload = (e) => {
        const content = `<a href="${e.target.result}" target="_blank">${file.name}</a>`;
        const value = e.target.result;
        updateFileList(file, content, value);
      };
    }
    reader.readAsDataURL(file);
  }

  function handleFileUploadClick() {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = "image/*, .txt, .md, .yaml, .csv, .docx, .pdf, .xlsx";
    input.multiple = false;
    input.style.display = "none";

    input.onchange = (e) => {
      const file = e.target.files[0];
      if (file) {
        handleInputChange(file);
      }
    };
    input.click();
  }

  function updateFileList(file, content, value) {
    const uploadedFile = {
      uid: Date.now() + Math.random(),
      file: file,
      content: content,
      value: value,
    };
    onFileChange(
      [...files, uploadedFile]
    );
  }

  function handleSubmit() {
    if (!sendButtonDisabled) {
      onSend(value, webSearchEnabled);
      onChange("");
    }
  }

  function handleKeyDown(e) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      handleSubmit();
    }
  }

  const isSpeechDisabled = false;

  return (
    <div className="absolute bottom-0 left-0 right-0 px-6 py-4 z-10">
      <UploadFileArea onFileChange={handleInputChange} />
      <div className="max-w-[700px] mx-auto">
        {files.length > 0 && (
          <div className="mb-3 mx-3">
            <ChatFileInput
              files={files}
              onFileChange={onFileChange}
            />
          </div>
        )}
        {webSearchEnabled && (
          <div className="mb-3 mx-3">
            <span className="inline-flex items-center gap-1.5 px-3 py-1 bg-secondary rounded-md text-xs text-primary">
              <Globe className="w-3 h-3" />
              <span>{i18next.t("chat:Web search")}</span>
              <button
                className="ml-0.5 p-0.5 rounded-full opacity-60 hover:opacity-100 hover:bg-muted transition-all"
                onClick={(e) => {
                  e.stopPropagation();
                  onWebSearchChange && onWebSearchChange(false);
                }}
              >
                <X className="w-2.5 h-2.5" />
              </button>
            </span>
          </div>
        )}

        {/* Input area */}
        <div className="flex items-end gap-2 rounded-lg border border-border bg-card p-2">
          {/* Menu button */}
          <ChatInputMenu
            disabled={disableInput || messageError}
            webSearchEnabled={webSearchEnabled}
            onWebSearchChange={onWebSearchChange}
            onFileUpload={handleFileUploadClick}
            disableFileUpload={store?.disableFileUpload}
            store={store}
            chat={chat}
          />

          {/* Textarea */}
          <textarea
            ref={textareaRef}
            className="flex-1 resize-none bg-transparent text-foreground placeholder:text-muted-foreground text-sm outline-none min-h-[2rem] max-h-[8rem] py-1.5"
            placeholder={messageError ? "" : i18next.t("chat:Type message here")}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={handleKeyDown}
            disabled={disableInput}
            rows={1}
            onInput={(e) => {
              e.target.style.height = "auto";
              e.target.style.height = Math.min(e.target.scrollHeight, 128) + "px";
            }}
          />

          {/* Voice input button */}
          {!isSpeechDisabled && (
            <button
              onClick={isVoiceInput ? onVoiceInputEnd : onVoiceInputStart}
              className={`p-1.5 rounded-md transition-colors ${
                isVoiceInput
                  ? "text-destructive hover:text-destructive/80"
                  : "text-muted-foreground hover:text-foreground hover:bg-secondary"
              }`}
            >
              {isVoiceInput ? <MicOff className="w-5 h-5" /> : <Mic className="w-5 h-5" />}
            </button>
          )}

          {/* Send / Cancel button */}
          {loading ? (
            <button
              onClick={() => onCancelMessage && onCancelMessage()}
              className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors"
            >
              <Square className="w-5 h-5" />
            </button>
          ) : (
            <button
              onClick={handleSubmit}
              disabled={sendButtonDisabled}
              className={`p-1.5 rounded-md transition-colors ${
                sendButtonDisabled
                  ? "text-muted-foreground/40 cursor-not-allowed"
                  : "text-primary hover:bg-primary/10"
              }`}
            >
              <Send className="w-5 h-5" />
            </button>
          )}
        </div>
      </div>
    </div>
  );
};

export default ChatInput;
