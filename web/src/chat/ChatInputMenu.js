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

import React, {useEffect, useRef, useState} from "react";
import {Check, Globe, Paperclip, Plus} from "lucide-react";
import i18next from "i18next";
import * as ProviderBackend from "../backend/ProviderBackend";
import * as Setting from "../Setting";

const ChatInputMenu = ({disabled, webSearchEnabled, onWebSearchChange, onFileUpload, disableFileUpload, store, chat}) => {
  const [webSearchSupported, setWebSearchSupported] = useState(false);
  const [open, setOpen] = useState(false);
  const prevModelProviderRef = useRef(null);

  const handleWebSearchToggle = () => {
    if (onWebSearchChange) {
      onWebSearchChange(!webSearchEnabled);
    }
    setOpen(false);
  };

  useEffect(() => {
    const modelProvider = chat?.modelProvider || store?.modelProvider;

    if (prevModelProviderRef.current !== null && prevModelProviderRef.current !== modelProvider) {
      if (webSearchEnabled && onWebSearchChange) {
        onWebSearchChange(false);
      }
    }
    prevModelProviderRef.current = modelProvider;

    if (!modelProvider) {
      setWebSearchSupported(false);
      return;
    }

    ProviderBackend.getProvider("admin", modelProvider)
      .then((res) => {
        if (res.status === "ok" && res.data) {
          setWebSearchSupported(Setting.isProviderSupportWebSearch(res.data));
        } else {
          setWebSearchSupported(false);
        }
      })
      .catch(() => {
        setWebSearchSupported(false);
      });
  }, [chat?.modelProvider, store?.modelProvider]);

  return (
    <div className="relative">
      <button
        onClick={() => !disabled && setOpen(!open)}
        disabled={disabled}
        className={`p-1.5 rounded-md transition-colors ${
          disabled
            ? "text-muted-foreground/40 cursor-not-allowed"
            : "text-muted-foreground hover:text-foreground hover:bg-secondary"
        }`}
      >
        <Plus className="w-5 h-5" />
      </button>

      {open && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute left-0 bottom-full mb-1 w-52 py-1 bg-popover border border-border rounded-lg shadow-xl z-50">
            <button
              onClick={() => {
                onFileUpload();
                setOpen(false);
              }}
              disabled={disableFileUpload}
              className={`w-full flex items-center gap-3 px-3 py-2.5 text-sm transition-colors ${
                disableFileUpload
                  ? "text-muted-foreground/40 cursor-not-allowed"
                  : "text-popover-foreground hover:bg-accent"
              }`}
            >
              <Paperclip className="w-4 h-4" />
              <span>{i18next.t("chat:Add attachment")}</span>
            </button>

            <button
              onClick={handleWebSearchToggle}
              disabled={!webSearchSupported}
              className={`w-full flex items-center justify-between px-3 py-2.5 text-sm transition-colors ${
                !webSearchSupported
                  ? "text-muted-foreground/40 cursor-not-allowed"
                  : "text-popover-foreground hover:bg-accent"
              }`}
            >
              <span className="flex items-center gap-3">
                <Globe className="w-4 h-4" />
                <span>{i18next.t("chat:Web search")}</span>
              </span>
              {webSearchEnabled && <Check className="w-3.5 h-3.5 text-blue-500" />}
            </button>
          </div>
        </>
      )}
    </div>
  );
};

export default ChatInputMenu;
