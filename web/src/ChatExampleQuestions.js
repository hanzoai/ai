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

import React, {useCallback, useEffect, useState} from "react";
import ImageWithFallback from "./ChatExampleQuestionIcon";
import * as Setting from "./Setting";
import {RefreshCw} from "lucide-react";
import i18next from "i18next";

const ChatExampleQuestions = ({sendMessage, exampleQuestions}) => {
  const [selected, setSelected] = useState([]);

  const selectExampleQuestions = useCallback(() => {
    const limit = Setting.isMobile() ? 4 : 8;
    if (exampleQuestions.length <= limit) {
      setSelected(exampleQuestions);
    } else {
      setSelected([...exampleQuestions].sort(() => 0.5 - Math.random()).slice(0, limit));
    }
  }, [exampleQuestions]);

  useEffect(() => {
    selectExampleQuestions();
  }, [selectExampleQuestions]);

  const limit = Setting.isMobile() ? 4 : 8;

  const groupedExampleQuestions = [];
  for (let i = 0; i < selected.length; i += 4) {
    groupedExampleQuestions.push(selected.slice(i, i + 4));
  }

  return (
    <div className="absolute inset-0 z-10 flex flex-col items-center justify-center pointer-events-none">
      <div className="pointer-events-auto w-4/5 max-w-3xl">
        {groupedExampleQuestions.map((group, gIdx) => (
          <div key={gIdx} className={`flex ${Setting.isMobile() ? "flex-col" : "flex-row"} justify-center items-center gap-3 mb-3`}>
            {group.map((q, qIdx) => (
              <div
                key={qIdx}
                className="w-[150px] p-2.5 rounded-lg bg-card border border-border shadow-sm cursor-pointer flex flex-col hover:bg-accent transition-colors"
                onClick={() => sendMessage(q.text, "")}
              >
                <ImageWithFallback src={q.image} />
                <p className="mt-2.5 text-sm leading-snug text-foreground line-clamp-2 h-[3em]">
                  {q.title}
                </p>
              </div>
            ))}
          </div>
        ))}
        {exampleQuestions.length > limit && (
          <div className="flex justify-center mt-5">
            <button
              onClick={selectExampleQuestions}
              className="inline-flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm text-primary-foreground hover:bg-primary/90 transition-colors"
            >
              <RefreshCw className="w-4 h-4" />
              {i18next.t("store:Refresh")}
            </button>
          </div>
        )}
      </div>
    </div>
  );
};

export default ChatExampleQuestions;
