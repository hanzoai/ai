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

import React, {useEffect, useState} from "react";
import {FileText, Loader2, X} from "lucide-react";
import i18next from "i18next";
import * as Setting from "../Setting";
import * as VectorBackend from "../backend/VectorBackend";

const KnowledgeSourceItem = ({vectorScore, vectorData, idx, account}) => {
  if (!vectorData) {
    return null;
  }

  const handleClick = () => {
    const selection = window.getSelection();
    const url = `/vectors/${vectorScore.vector}${Setting.isLocalAdminUser(account) ? "" : "?mode=view"}`;
    if (selection.toString().length > 0) {
      return;
    }

    if (vectorScore.vector) {
      Setting.openLink(url);
    }
  };

  const handleFileOpen = (e) => {
    e.stopPropagation();
    if (vectorData.store && vectorData.file) {
      const fileKey = encodeURIComponent(vectorData.file.replace(/^\/+/, ""));
      Setting.openLink(`/stores/${vectorData.owner || "admin"}/${vectorData.store}/view?fileKey=${fileKey}`);
    }
  };

  return (
    <div className="p-3 bg-card rounded-lg border border-border transition-all hover:shadow-lg hover:border-primary/50">
      <div className="flex items-center gap-2 mb-3">
        <span
          className="inline-flex items-center justify-center w-5 h-5 bg-primary text-primary-foreground rounded-full text-xs font-bold cursor-pointer"
          onClick={handleClick}
        >
          {idx + 1}
        </span>
        <span
          className="text-muted-foreground cursor-pointer hover:text-foreground"
          onClick={handleFileOpen}
        >
          <FileText className="w-5 h-5" />
        </span>
        <span
          className="text-primary text-[13px] font-medium flex-1 cursor-pointer underline hover:text-primary/80"
          onClick={handleFileOpen}
        >
          {vectorData.file || i18next.t("chat:Knowledge Fragment")}
        </span>
        <span className="px-2 py-0.5 bg-secondary rounded text-xs text-muted-foreground font-medium">
          {i18next.t("chat:Relevance")}: {(vectorScore.score * 100).toFixed(1)}%
        </span>
      </div>
      <div
        className="text-[13px] text-muted-foreground leading-relaxed whitespace-pre-line break-words max-h-[800px] overflow-auto max-w-[800px] min-w-[400px] cursor-pointer"
        onClick={handleClick}
      >
        {vectorData.text}
      </div>
    </div>
  );
};

const KnowledgeSourcesDrawer = ({visible, onClose, vectorScores, account}) => {
  const [loading, setLoading] = useState(false);
  const [vectorsData, setVectorsData] = useState({});

  useEffect(() => {
    const loadVectorsData = async() => {
      setLoading(true);
      const newVectorsData = {};

      try {
        for (const vectorScore of vectorScores) {
          if (!vectorScore.vector || typeof vectorScore.vector !== "string") {
            continue;
          }
          const result = await VectorBackend.getVector("admin", vectorScore.vector);
          if (result.status === "ok" && result.data) {
            newVectorsData[vectorScore.vector] = result.data;
          }
        }
        setVectorsData(newVectorsData);
      } catch (error) {
        Setting.showMessage("error", i18next.t("chat:Unable to load knowledge base sources. Please try again."));
      } finally {
        setLoading(false);
      }
    };

    if (visible && vectorScores && vectorScores.length > 0) {
      loadVectorsData();
    }
  }, [visible, vectorScores]);

  if (!visible) {return null;}

  return (
    <>
      {/* Backdrop */}
      <div className="fixed inset-0 bg-black/50 z-40" onClick={onClose} />

      {/* Slide-over panel */}
      <div className="fixed inset-y-0 right-0 z-50 w-auto max-w-[90vw] bg-background border-l border-border shadow-xl flex flex-col transform transition-transform duration-300">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <h3 className="text-sm font-semibold text-foreground">{i18next.t("chat:Knowledge sources")}</h3>
          <button
            onClick={onClose}
            className="p-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-4">
          {loading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="w-6 h-6 animate-spin text-muted-foreground" />
            </div>
          ) : (
            vectorScores?.length > 0 && (
              <div className="flex flex-col gap-5">
                {vectorScores.map((vectorScore, idx) => (
                  <KnowledgeSourceItem
                    key={vectorScore.vector}
                    vectorScore={vectorScore}
                    vectorData={vectorsData[vectorScore.vector]}
                    idx={idx}
                    account={account}
                  />
                ))}
              </div>
            )
          )}
        </div>
      </div>
    </>
  );
};

export default KnowledgeSourcesDrawer;
