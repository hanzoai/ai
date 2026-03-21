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
import * as Setting from "./Setting";
import * as VectorBackend from "./backend/VectorBackend";
import i18next from "i18next";

const VectorTooltip = ({vectorScore, children}) => {
  const [vectorData, setVectorData] = useState(null);
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const fetchVectorInfo = async() => {
      try {
        const res = await VectorBackend.getVector("admin", vectorScore.vector);
        if (res.status === "ok") {
          setVectorData(res.data);
        }
      } catch (error) {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      }
    };

    fetchVectorInfo();
  }, [vectorScore.vector]);

  return (
    <div
      className="relative inline-block"
      onMouseEnter={() => setVisible(true)}
      onMouseLeave={() => setVisible(false)}
    >
      {children}
      {visible && vectorData && (
        <div className="absolute right-full top-0 mr-2 z-50 w-[500px] max-w-[800px] rounded-lg border border-border bg-card p-3 shadow-xl text-sm">
          <div className="flex flex-wrap gap-3 text-muted-foreground">
            <span><strong className="text-foreground">{i18next.t("general:Name")}:</strong> {vectorScore.vector}</span>
            <span><strong className="text-foreground">{i18next.t("video:Score")}:</strong> {vectorScore.score}</span>
            <span><strong className="text-foreground">{i18next.t("store:File")}:</strong> {vectorData?.file}</span>
          </div>
          <div className="mt-2 pt-2 border-t border-border">
            <div className="max-h-[500px] overflow-auto text-[13px] bg-secondary text-foreground p-2 rounded whitespace-pre-wrap border border-border">
              {vectorData?.text}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default VectorTooltip;
