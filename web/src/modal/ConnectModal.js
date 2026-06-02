// Copyright 2024 Hanzo AI Inc. All Rights Reserved.
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
import i18next from "i18next";
import {User, Lock} from "lucide-react";
import * as Setting from "../Setting";

const ConnectModal = (props) => {
  const text = props.text ? props.text : i18next.t("node:Connect");
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [inputDisabled, setInputDisabled] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const owner = props.owner;
  const name = props.name;
  const category = props.category;
  const node = props.node || {};

  const handleUsernameAndPassword = (username, password) => {
    setUsername(username || "");
    if (username && password) {
      handleOk();
    } else {
      if (username) {
        setInputDisabled(true);
      }
      setIsModalOpen(true);
    }
  };

  const showModal = () => {
    initStatus();
    handleUsernameAndPassword(node.remoteUsername, node.remotePassword);
  };

  const handleOk = () => {
    setIsModalOpen(false);
    if (category === "Node") {
      const link = (username === "" || password === "") ? `access/${owner}/${name}` : `access/${owner}/${name}?username=${username}&password=${password}`;
      Setting.openLink(link);
    } else if (category === "Database") {
      const link = "databases";
      Setting.openLink(link);
    } else {
      Setting.showMessage("error", `${i18next.t("general:Unknown category")}: ${category}`);
    }
  };

  const handleCancel = () => {
    setIsModalOpen(false);
  };

  const initStatus = () => {
    setInputDisabled(false);
    setIsModalOpen(false);
    setUsername("");
    setPassword("");
  };

  return (
    <>
      <button
        disabled={props.disabled}
        onClick={showModal}
        className="px-3 py-1.5 bg-white text-black rounded text-xs font-medium hover:bg-zinc-200 transition-colors disabled:opacity-50 mr-2 my-1"
      >
        {text}
      </button>
      {isModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60" onClick={handleCancel}>
          <div className="bg-zinc-900 border border-zinc-800 rounded-lg p-6 w-[420px]" onClick={(e) => e.stopPropagation()}>
            <h3 className="text-base font-medium text-white mb-4">Connect</h3>
            <div className="space-y-3">
              <div className="relative">
                <User className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-zinc-500" />
                <input
                  className="w-full pl-9 pr-3 py-2.5 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50"
                  placeholder={i18next.t("general:Username")}
                  value={username}
                  disabled={inputDisabled}
                  onChange={e => setUsername(e.target.value)}
                />
              </div>
              <div className="relative">
                <Lock className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-zinc-500" />
                <input
                  type="password"
                  className="w-full pl-9 pr-3 py-2.5 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500"
                  placeholder={i18next.t("general:Password")}
                  value={password}
                  onChange={e => setPassword(e.target.value)}
                />
              </div>
              <button
                disabled={!username || !password}
                onClick={handleOk}
                className="w-full py-2.5 bg-white text-black rounded text-sm font-medium hover:bg-zinc-200 transition-colors disabled:opacity-50"
              >
                login
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
};

export default ConnectModal;
