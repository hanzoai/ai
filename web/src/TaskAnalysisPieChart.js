// Copyright 2023 The Casibase Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use it except in compliance with the License.
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
import ReactEcharts from "echarts-for-react";
import i18next from "i18next";

const PIE_COLORS = ["#f5222d", "#fa8c16", "#faad14", "#52c41a", "#1677ff"];
const SCORE_BANDS = [
  {min: 0, max: 10, label: "0-10"},
  {min: 10, max: 20, label: "10-20"},
  {min: 20, max: 30, label: "20-30"},
  {min: 30, max: 40, label: "30-40"},
  {min: 40, max: 51, label: "40-50"},
];

function countByScoreBand(categories) {
  const counts = SCORE_BANDS.map(() => 0);
  (categories || []).forEach((cat) => {
    (cat.items || []).forEach((item) => {
      const s = Number(item.score) || 0;
      const idx = SCORE_BANDS.findIndex((b) => s >= b.min && s < b.max);
      if (idx >= 0) {
        counts[idx] += 1;
      }
    });
  });
  return SCORE_BANDS.map((b, i) => ({
    band: b.label,
    count: counts[i],
  })).filter((d) => d.count > 0);
}

export default function TaskAnalysisPieChart({categories}) {
  const bandData = countByScoreBand(categories);
  if (bandData.length === 0) {
    return null;
  }
  const totalItems = bandData.reduce((sum, d) => sum + d.count, 0);
  const scoreUnit = i18next.t("task:Score Unit");
  const data = bandData.map((d, i) => ({
    name: `${d.band}${scoreUnit} (${d.count}${i18next.t("task:Item count unit")})`,
    value: d.count,
    itemStyle: {color: PIE_COLORS[i % PIE_COLORS.length]},
  }));
  const option = {
    tooltip: {
      trigger: "item",
      formatter: "{b}: {c} ({d}%)",
    },
    legend: {
      orient: "vertical",
      right: "8%",
      top: "center",
      textStyle: {fontSize: 12, color: "#000"},
    },
    title: {
      left: "center",
      top: "8%",
      textStyle: {fontSize: 13, color: "#000"},
      text: i18next.t("task:Score distribution") + ` (${totalItems})`,
    },
    series: [{
      type: "pie",
      radius: ["40%", "70%"],
      center: ["40%", "55%"],
      avoidLabelOverlap: true,
      itemStyle: {borderColor: "#fff", borderWidth: 2},
      label: {fontSize: 12, color: "#000"},
      data,
    }],
  };
  return <ReactEcharts option={option} style={{width: "100%", height: "100%"}} notMerge />;
}
