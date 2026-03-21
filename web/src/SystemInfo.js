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

import React, {useCallback, useEffect, useRef, useState} from "react";
import {Loader2} from "lucide-react";
import * as SystemBackend from "./backend/SystemInfo";
import * as Setting from "./Setting";
import i18next from "i18next";
import PrometheusInfoTable from "./table/PrometheusInfoTable";

function ProgressBar({percent}) {
  const color = percent > 80 ? "bg-red-500" : percent > 60 ? "bg-yellow-500" : "bg-green-500";
  return (
    <div className="w-full bg-zinc-800 rounded-full h-2">
      <div className={`h-2 rounded-full transition-all ${color}`} style={{width: `${Math.min(percent, 100)}%`}} />
    </div>
  );
}

function CircularProgress({percent}) {
  const radius = 45;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference - (percent / 100) * circumference;
  const color = percent > 80 ? "stroke-red-500" : percent > 60 ? "stroke-yellow-500" : "stroke-green-500";

  return (
    <div className="relative inline-flex items-center justify-center w-28 h-28">
      <svg className="w-28 h-28 -rotate-90">
        <circle cx="56" cy="56" r={radius} fill="none" stroke="currentColor" strokeWidth="8" className="text-zinc-800" />
        <circle cx="56" cy="56" r={radius} fill="none" strokeWidth="8" strokeDasharray={circumference} strokeDashoffset={offset} strokeLinecap="round" className={color} />
      </svg>
      <span className="absolute text-lg font-semibold text-white">{percent}%</span>
    </div>
  );
}

function InfoCard({id, title, loading, children}) {
  return (
    <div id={id} className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-6">
      <h3 className="text-sm font-medium text-zinc-400 mb-4">{title}</h3>
      {loading ? (
        <div className="flex justify-center py-8">
          <Loader2 className="w-8 h-8 text-zinc-500 animate-spin" />
        </div>
      ) : (
        <div className="text-center">{children}</div>
      )}
    </div>
  );
}

function SystemInfo() {
  const [systemInfo, setSystemInfo] = useState({
    cpuUsage: [], memoryUsed: 0, memoryTotal: 0, diskUsed: 0, diskTotal: 0,
    networkSent: 0, networkRecv: 0, networkTotal: 0,
  });
  const [versionInfo, setVersionInfo] = useState({});
  const [prometheusInfo, setPrometheusInfo] = useState({apiThroughput: [], apiLatency: [], totalThroughput: 0});
  const [loading, setLoading] = useState(true);
  const intervalRef = useRef(null);

  const stopTimer = useCallback(() => {
    if (intervalRef.current !== null) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
  }, []);

  useEffect(() => {
    SystemBackend.getSystemInfo("").then(res => {
      setLoading(false);
      if (res.status === "ok") {
        setSystemInfo(res.data);
      } else {
        Setting.showMessage("error", res.msg);
        stopTimer();
      }

      const id = setInterval(() => {
        SystemBackend.getSystemInfo("").then(res => {
          setLoading(false);
          if (res.status === "ok") {
            setSystemInfo(res.data);
          } else {
            Setting.showMessage("error", res.msg);
            stopTimer();
          }
        }).catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${error}`);
          stopTimer();
        });
        SystemBackend.getPrometheusInfo().then(res => {
          setPrometheusInfo(res.data);
        });
      }, 2000);

      intervalRef.current = id;
    }).catch(error => {
      Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${error}`);
      stopTimer();
    });

    SystemBackend.getVersionInfo().then(res => {
      if (res.status === "ok") {
        setVersionInfo(res.data);
      } else {
        Setting.showMessage("error", res.msg);
      }
    }).catch(err => {
      Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${err}`);
    });

    return () => stopTimer();
  }, [stopTimer]);

  const cpuUi = systemInfo.cpuUsage?.length <= 0
    ? <p className="text-zinc-500">{i18next.t("system:Failed to get CPU usage")}</p>
    : (
      <div className="space-y-2">
        {systemInfo.cpuUsage.map((usage, i) => (
          <div key={i} className="flex items-center gap-3">
            <span className="text-xs text-zinc-500 w-12">Core {i}</span>
            <div className="flex-1">
              <ProgressBar percent={Number(usage.toFixed(1))} />
            </div>
            <span className="text-xs text-zinc-400 w-12 text-right">{usage.toFixed(1)}%</span>
          </div>
        ))}
      </div>
    );

  const memPercent = systemInfo.memoryTotal > 0
    ? Number((systemInfo.memoryUsed / systemInfo.memoryTotal * 100).toFixed(2))
    : 0;
  const memUi = systemInfo.memoryTotal <= 0
    ? <p className="text-zinc-500">{i18next.t("system:Failed to get memory usage")}</p>
    : (
      <div>
        <p className="text-sm text-zinc-300 mb-3">
          {Setting.getFriendlyFileSize(systemInfo.memoryUsed)} / {Setting.getFriendlyFileSize(systemInfo.memoryTotal)}
        </p>
        <CircularProgress percent={memPercent} />
      </div>
    );

  const diskPercent = systemInfo.diskTotal > 0
    ? Number((systemInfo.diskUsed / systemInfo.diskTotal * 100).toFixed(2))
    : 0;
  const diskUi = systemInfo.diskTotal <= 0
    ? <p className="text-zinc-500">{i18next.t("system:Failed to get disk usage")}</p>
    : (
      <div>
        <p className="text-sm text-zinc-300 mb-3">
          {Setting.getFriendlyFileSize(systemInfo.diskUsed)} / {Setting.getFriendlyFileSize(systemInfo.diskTotal)}
        </p>
        <CircularProgress percent={diskPercent} />
      </div>
    );

  const networkUi = systemInfo.networkTotal === undefined || systemInfo.networkTotal === null
    ? <p className="text-zinc-500">{i18next.t("system:Failed to get network usage")}</p>
    : (
      <div className="space-y-2">
        <p className="text-sm text-zinc-300">
          {i18next.t("system:Sent")}: {Setting.getFriendlyFileSize(systemInfo.networkSent)}
        </p>
        <p className="text-sm text-zinc-300">
          {i18next.t("system:Received")}: {Setting.getFriendlyFileSize(systemInfo.networkRecv)}
        </p>
        <p className="text-base font-semibold text-white mt-3">
          {i18next.t("system:Total Throughput")}: {Setting.getFriendlyFileSize(systemInfo.networkTotal)}
        </p>
      </div>
    );

  const latencyUi = !prometheusInfo?.apiLatency || prometheusInfo.apiLatency.length <= 0
    ? <Loader2 className="w-8 h-8 text-zinc-500 animate-spin mx-auto" />
    : <PrometheusInfoTable prometheusInfo={prometheusInfo} table={"latency"} />;

  const throughputUi = !prometheusInfo?.apiThroughput || prometheusInfo.apiThroughput.length <= 0
    ? <Loader2 className="w-8 h-8 text-zinc-500 animate-spin mx-auto" />
    : <PrometheusInfoTable prometheusInfo={prometheusInfo} table={"throughput"} />;

  const link = versionInfo?.version !== ""
    ? `https://github.com/hanzoai/cloud/releases/tag/${versionInfo?.version}`
    : "";
  let versionText = versionInfo?.version !== ""
    ? versionInfo?.version
    : i18next.t("system:Unknown version");
  if (versionInfo?.commitOffset > 0) {
    versionText += ` (ahead+${versionInfo.commitOffset})`;
  }

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
        <InfoCard id="network-card" title={i18next.t("system:Network Usage")} loading={loading}>
          {networkUi}
        </InfoCard>
        <InfoCard id="cpu-card" title={i18next.t("system:CPU Usage")} loading={loading}>
          {cpuUi}
        </InfoCard>
        <InfoCard id="memory-card" title={i18next.t("system:Memory Usage")} loading={loading}>
          {memUi}
        </InfoCard>
        <InfoCard id="disk-card" title={i18next.t("system:Disk Usage")} loading={loading}>
          {diskUi}
        </InfoCard>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <InfoCard id="latency-card" title={i18next.t("system:API Latency")} loading={loading}>
          {latencyUi}
        </InfoCard>
        <InfoCard id="throughput-card" title={i18next.t("system:API Throughput")} loading={loading}>
          {throughputUi}
        </InfoCard>
      </div>

      <InfoCard id="about-card" title={i18next.t("system:About Hanzo Cloud")} loading={false}>
        <p className="text-sm text-zinc-300 mb-3">
          {i18next.t("system:AI Knowledge Database & Chat Bot with Admin UI and multi-model support (ChatGPT, Claude, Llama 3, DeepSeek R1, HuggingFace, etc.)")}
        </p>
        <div className="space-y-1 text-sm text-zinc-400">
          <p>
            GitHub: <a target="_blank" rel="noreferrer" href="https://github.com/hanzoai/cloud" className="text-blue-400 hover:text-blue-300">Hanzo Cloud</a>
          </p>
          <p>
            {i18next.t("general:Version")}: <a target="_blank" rel="noreferrer" href={link} className="text-blue-400 hover:text-blue-300">{versionText}</a>
          </p>
          <p>
            {i18next.t("system:Official website")}: <a target="_blank" rel="noreferrer" href="https://hanzo.ai" className="text-blue-400 hover:text-blue-300">https://hanzo.ai</a>
          </p>
          <p>
            {i18next.t("system:Community")}: <a target="_blank" rel="noreferrer" href="https://github.com/hanzoai/cloud/discussions" className="text-blue-400 hover:text-blue-300">Get in Touch!</a>
          </p>
        </div>
      </InfoCard>
    </div>
  );
}

export default SystemInfo;
