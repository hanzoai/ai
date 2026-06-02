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

import React from "react";
import i18next from "i18next";
import * as Setting from "./Setting";
import * as Conf from "./Conf";

class GraphChatTable extends React.Component {
  render() {
    const {chats} = this.props;

    if (!chats || chats.length === 0) {
      return null;
    }

    const columns = [
      {title: i18next.t("general:Name"), dataIndex: "name", key: "name", render: (text) => <a target="_blank" rel="noreferrer" href={`/chats/${text}`} className="text-blue-400 hover:text-blue-300">{text}</a>},
      {title: i18next.t("general:Display name"), dataIndex: "displayName", key: "displayName", render: (text, record) => <a target="_blank" rel="noreferrer" href={`/chats/${record.name}`} className="text-blue-400 hover:text-blue-300">{text}</a>},
      {title: i18next.t("general:User"), dataIndex: "user", key: "user", render: (text) => {
        if (text.startsWith("u-")) {return text;}
        return <a target="_blank" rel="noreferrer" href={Setting.getMyProfileUrl(this.props.account).replace("/account", `/users/${Conf.AuthConfig.organizationName}/${text}`)} className="text-blue-400 hover:text-blue-300">{text}</a>;
      }},
      {title: i18next.t("general:Created time"), dataIndex: "createdTime", key: "createdTime", render: (text) => Setting.getFormattedDate(text)},
      {title: i18next.t("general:Updated time"), dataIndex: "updatedTime", key: "updatedTime", render: (text) => Setting.getFormattedDate(text)},
      {title: i18next.t("store:Message count"), dataIndex: "messageCount", key: "messageCount"},
      {title: i18next.t("chat:Token count"), dataIndex: "tokenCount", key: "tokenCount"},
    ];

    return (
      <div className="mt-5 bg-zinc-900/50 border border-zinc-800 rounded-lg">
        <div className="px-4 py-3 border-b border-zinc-800">
          <h3 className="text-sm font-medium text-white">{i18next.t("general:Chats")}</h3>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm text-left">
            <thead className="bg-zinc-900/80 border-b border-zinc-800">
              <tr>
                {columns.map(col => <th key={col.key} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800/50">
              {chats.map((record) => (
                <tr key={record.name} className="hover:bg-zinc-900/50 transition-colors">
                  {columns.map(col => <td key={col.key} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record) : record[col.dataIndex]}</td>)}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    );
  }
}

export default GraphChatTable;
