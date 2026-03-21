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

function DataTable({columns, data, rowKey = "name"}) {
  return (
    <div className="overflow-x-auto border border-zinc-800 rounded-lg">
      <table className="w-full text-sm text-left">
        <thead className="bg-zinc-900/80 border-b border-zinc-800">
          <tr>
            {columns.map(col => (
              <th key={col.key || col.dataIndex} className="px-3 py-2 text-xs font-medium text-zinc-400 whitespace-nowrap">
                {col.title}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/50">
          {(data || []).map((record, index) => (
            <tr key={record[rowKey] || index} className="hover:bg-zinc-900/50 transition-colors">
              {columns.map(col => (
                <td key={col.key || col.dataIndex} className="px-3 py-2 text-zinc-300 whitespace-nowrap">
                  {col.render ? col.render(record[col.dataIndex], record, index) : record[col.dataIndex]}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function PrometheusInfoTable({prometheusInfo, table}) {
  const latencyColumns = [
    {title: i18next.t("general:Name"), dataIndex: "name", key: "name"},
    {title: i18next.t("general:Method"), dataIndex: "method", key: "method"},
    {title: i18next.t("general:Count"), dataIndex: "count", key: "count"},
    {title: i18next.t("scan:Latency") + "(ms)", dataIndex: "latency", key: "latency"},
  ];

  const throughputColumns = [
    {title: i18next.t("general:Name"), dataIndex: "name", key: "name"},
    {title: i18next.t("general:Method"), dataIndex: "method", key: "method"},
    {title: i18next.t("system:Throughput"), dataIndex: "throughput", key: "throughput"},
  ];

  if (table === "latency") {
    return (
      <div style={{height: "300px", overflow: "auto"}}>
        <DataTable columns={latencyColumns} data={prometheusInfo.apiLatency} />
      </div>
    );
  }

  if (table === "throughput") {
    return (
      <div style={{height: "300px", overflow: "auto"}}>
        <p className="text-sm text-zinc-300 mb-2">
          {i18next.t("system:Total Throughput")}: {prometheusInfo.totalThroughput}
        </p>
        <DataTable columns={throughputColumns} data={prometheusInfo.apiThroughput} />
      </div>
    );
  }

  return null;
}

export default PrometheusInfoTable;
