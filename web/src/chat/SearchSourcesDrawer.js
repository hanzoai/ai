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
import {X} from "lucide-react";
import i18next from "i18next";

const SearchResultItem = ({result, idx}) => {
  const [iconError, setIconError] = useState(false);

  return (
    <div
      role="button"
      tabIndex={0}
      className="p-3 bg-card rounded-lg border border-border cursor-pointer transition-all hover:shadow-lg hover:border-primary/50"
      onClick={() => {
        if (result.url) {
          window.open(result.url, "_blank");
        }
      }}
      onKeyDown={(e) => {
        if ((e.key === "Enter" || e.key === " ") && result.url) {
          e.preventDefault();
          window.open(result.url, "_blank");
        }
      }}
    >
      <div className="flex items-center gap-2 mb-1.5">
        {result.icon && !iconError ? (
          <img
            src={result.icon}
            alt="site icon"
            className="w-5 h-5 rounded-full object-cover"
            onError={() => setIconError(true)}
          />
        ) : (
          <span className="inline-flex items-center justify-center w-5 h-5 bg-primary text-primary-foreground rounded-full text-xs font-bold">
            {result.index || idx + 1}
          </span>
        )}
        <span className="text-xs font-medium text-primary">
          {result.site_name || (result.url ? new URL(result.url).hostname : "")}
        </span>
      </div>
      <div className="text-sm text-foreground mb-1.5 font-medium leading-snug">
        {result.title}
      </div>
      <div className="text-xs text-muted-foreground overflow-hidden text-ellipsis whitespace-nowrap">
        {result.url}
      </div>
    </div>
  );
};

const SearchSourcesDrawer = ({visible, onClose, searchResults}) => {
  if (!visible) {return null;}

  return (
    <>
      {/* Backdrop */}
      <div className="fixed inset-0 bg-black/50 z-40" onClick={onClose} />

      {/* Slide-over panel */}
      <div className="fixed inset-y-0 right-0 z-50 w-[400px] max-w-[90vw] bg-background border-l border-border shadow-xl flex flex-col transform transition-transform duration-300">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <h3 className="text-sm font-semibold text-foreground">{i18next.t("chat:Web sources")}</h3>
          <button
            onClick={onClose}
            className="p-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-4">
          {searchResults?.length > 0 && (
            <div className="flex flex-col gap-3">
              {searchResults.map((result, idx) => (
                <SearchResultItem
                  key={idx}
                  result={result}
                  idx={idx}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </>
  );
};

export default SearchSourcesDrawer;
