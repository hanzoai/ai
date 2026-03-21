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

import React from "react";
import i18next from "i18next";
import * as Setting from "./Setting";

const UsageTable = ({data, account}) => {
  let columns = [
    {
      title: i18next.t("general:User"),
      dataIndex: "user",
      key: "user",
      width: "12%",
    },
    {
      title: i18next.t("general:Chats"),
      dataIndex: "chats",
      key: "chats",
      render: (text, record) => (
        <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs"> {
            Setting.goToLink(`/chats?user=${record.user}`);
          }}
        >
          {text}
        </span>
      ),
      width: "15%",
      sorter: (a, b) => a.chats - b.chats,
    },
    {
      title: i18next.t("general:Messages"),
      dataIndex: "messageCount",
      key: "message",
      render: (text, record) => (
        <span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs"> {
            Setting.goToLink(`/messages?user=${record.user}`);
          }}
        >
          {text}
        </span>
      ),
      width: "15%",
      sorter: (a, b) => a.messageCount - b.messageCount,
    },
    {
      title: i18next.t("chat:Token count"),
      dataIndex: "tokenCount",
      key: "token",
      width: "15%",
      defaultSortOrder: "descend",
      sorter: (a, b) => a.tokenCount - b.tokenCount,
    },
    {
      title: i18next.t("chat:Price"),
      dataIndex: "price",
      key: "price",
      width: "15%",
      sorter: (a, b) => a.price - b.price,
    },
  ];

  if (!account || account.name !== "admin") {
    columns = columns.filter(column => column.key !== "tokenCount" && column.key !== "price");
  }

  return (
    <div style={{margin: "20px", marginLeft: "60px", marginRight: "60px"}}>
      <span style={{display: "block", fontSize: "20px", marginBottom: "10px"}}>
        {i18next.t("general:Users")}
      </span>
      <div className="overflow-x-auto border border-zinc-800 rounded-lg"><table className="w-full text-sm text-left"><thead className="bg-zinc-900/80 border-b border-zinc-800"><tr>{columns.map(col => <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">{col.title}</th>)}</tr></thead><tbody className="divide-y divide-zinc-800/50">{(data || []).map((record, index) => <tr key={typeof "name" === "function" ? ("name")(record) : record["name"] || index} className="hover:bg-zinc-900/50 transition-colors">{columns.map(col => <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">{col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}</td>)}</tr>)}</tbody></table></div>
    </div>
  );
};

export default UsageTable;
