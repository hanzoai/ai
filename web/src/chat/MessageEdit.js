// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
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

import React, {useState} from "react";
import {Check, Pencil, X} from "lucide-react";
import i18next from "i18next";

const MessageEdit = ({
  message,
  isLastMessage,
  disableInput,
  index,
  onEditMessage,
}) => {
  const [isEditing, setIsEditing] = useState(false);
  const [editedText, setEditedText] = useState("");
  const [isHovering, setIsHovering] = useState(false);

  const handleEditActions = {
    start: () => {
      setIsEditing(true);
      setEditedText(message.text);
    },
    save: () => {
      onEditMessage({...message, text: editedText, updatedTime: new Date().toISOString()});
      setIsEditing(false);
    },
    cancel: () => {
      setIsEditing(false);
      setEditedText("");
    },
    keyDown: (e) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        handleEditActions.save();
      }
    },
  };

  const renderEditForm = () => (
    <div className="w-full">
      <textarea
        value={editedText}
        onChange={e => setEditedText(e.target.value)}
        onKeyDown={handleEditActions.keyDown}
        className="w-full min-h-[2.5rem] max-h-[12rem] resize-y rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring mb-2"
        autoFocus
        rows={1}
      />
      <div className="flex justify-end gap-2">
        <button
          onClick={handleEditActions.cancel}
          className="inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors"
        >
          <X className="w-3 h-3" />
          {i18next.t("general:Cancel")}
        </button>
        <button
          onClick={handleEditActions.save}
          className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs text-primary-foreground hover:bg-primary/90 transition-colors"
        >
          <Check className="w-3 h-3" />
          {i18next.t("general:Save")}
        </button>
      </div>
    </div>
  );

  const renderEditButton = () => {
    if (message.author !== "AI" && !isEditing && (disableInput === false || index !== isLastMessage)) {
      return (
        <div
          className="mr-2 transition-opacity duration-200"
          style={{opacity: isHovering ? 0.8 : 0}}
        >
          <button
            className="flex items-center justify-center w-8 h-8 rounded-full bg-card/80 shadow-md text-primary hover:bg-card transition-colors"
            onClick={handleEditActions.start}
          >
            <Pencil className="w-3.5 h-3.5" />
          </button>
        </div>
      );
    }
    return null;
  };

  return {
    isEditing,
    editedText,
    setIsHovering,
    renderEditForm,
    renderEditButton,
    handleMouseEnter: () => setIsHovering(true),
    handleMouseLeave: () => setIsHovering(false),
  };
};

export default MessageEdit;
