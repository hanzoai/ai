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
import {withRouter} from "react-router-dom";
import {AlertCircle, HelpCircle, Info, Loader2} from "lucide-react";
import * as Setting from "./Setting";
import i18next from "i18next";

function AuthCallback() {
  const [msg, setMsg] = useState(null);
  const [errorDetails, setErrorDetails] = useState(null);
  const [showDetailsModal, setShowDetailsModal] = useState(false);

  useEffect(() => {
    login();
  }, []);

  function getFromLink() {
    const from = sessionStorage.getItem("from");
    return from === null ? "/" : from;
  }

  function login() {
    Setting.signin().then((res) => {
      if (res.status === "ok") {
        Setting.showMessage("success", i18next.t("general:Successfully logged in"));
        const link = getFromLink();
        Setting.goToLink(link);
      } else {
        setMsg(res.msg);
        setErrorDetails(res);
      }
    });
  }

  function renderErrorDetails() {
    if (!errorDetails) {return null;}

    const details = [];
    if (errorDetails.msg) {
      details.push({label: i18next.t("login:Error Message"), value: errorDetails.msg});
    }
    if (errorDetails.data) {
      details.push({
        label: i18next.t("login:Additional Information"),
        value: typeof errorDetails.data === "string" ? errorDetails.data : JSON.stringify(errorDetails.data, null, 2),
      });
    }
    if (errorDetails.data2) {
      details.push({
        label: i18next.t("login:More Details"),
        value: typeof errorDetails.data2 === "string" ? errorDetails.data2 : JSON.stringify(errorDetails.data2, null, 2),
      });
    }

    return (
      <div className="text-left space-y-4">
        {details.map((detail, index) => (
          <div key={index}>
            <p className="text-sm font-semibold text-zinc-300 mb-1">{detail.label}:</p>
            <pre className="text-xs text-zinc-400 bg-zinc-900 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre-wrap break-words">
              {detail.value}
            </pre>
          </div>
        ))}
      </div>
    );
  }

  if (msg === null) {
    return (
      <div className="flex flex-col items-center justify-center py-20">
        <Loader2 className="w-12 h-12 text-zinc-500 animate-spin mb-4" />
        <p className="text-zinc-400">{i18next.t("login:Signing in...")}</p>
      </div>
    );
  }

  return (
    <div className="flex flex-col items-center justify-center py-20">
      <AlertCircle className="w-16 h-16 text-red-500 mb-4" />
      <h1 className="text-2xl font-bold text-white mb-2">{i18next.t("login:Login Error")}</h1>
      <p className="text-zinc-400 mb-6 max-w-md text-center">{msg}</p>

      <div className="flex gap-3">
        <button
          onClick={() => setShowDetailsModal(true)}
          className="flex items-center gap-2 px-4 py-2 bg-white text-black rounded-md text-sm font-medium hover:bg-zinc-200 transition-colors"
        >
          <Info className="w-4 h-4" />
          {i18next.t("login:Details")}
        </button>
        <button
          onClick={() => window.open("https://hanzo.ai/docs/cloud", "_blank")}
          className="flex items-center gap-2 px-4 py-2 bg-zinc-800 text-zinc-300 rounded-md text-sm hover:bg-zinc-700 transition-colors"
        >
          <HelpCircle className="w-4 h-4" />
          {i18next.t("login:Help")}
        </button>
      </div>

      {/* Details modal */}
      {showDetailsModal && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4">
          <div className="bg-zinc-900 border border-zinc-800 rounded-lg shadow-xl max-w-2xl w-full max-h-[80vh] overflow-y-auto">
            <div className="flex items-center justify-between p-4 border-b border-zinc-800">
              <h2 className="text-lg font-semibold text-white">{i18next.t("login:Error Details")}</h2>
              <button
                onClick={() => setShowDetailsModal(false)}
                className="text-zinc-500 hover:text-white transition-colors"
              >
                &times;
              </button>
            </div>
            <div className="p-4">
              {renderErrorDetails()}
            </div>
            <div className="flex justify-end p-4 border-t border-zinc-800">
              <button
                onClick={() => setShowDetailsModal(false)}
                className="px-4 py-2 bg-white text-black rounded-md text-sm font-medium hover:bg-zinc-200 transition-colors"
              >
                {i18next.t("general:Close")}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export default withRouter(AuthCallback);
