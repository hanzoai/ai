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

// Copyright 2025 Hanzo AI Inc. All Rights Reserved.

import React from "react";
import * as ApplicationBackend from "./backend/ApplicationBackend";
import * as Setting from "./Setting";
import EventTable from "./table/EventTable";
import DeploymentTable from "./table/DeploymentTable";
import CredentialsTable from "./table/CredentialTable";
import i18next from "i18next";
import copy from "copy-to-clipboard";


class ApplicationViewPage extends React.Component {
  constructor(props) {
    super(props);
    const application = props.application || (props.location && props.location.state ? props.location.state.application : null);
    this.state = {
      application: application,
      loading: false,
      error: null,
    };
  }

  componentDidMount() {
    this.getApplication();
  }

  componentDidUpdate(prevProps) {
    if (prevProps.application !== this.props.application) {
      this.setState({application: this.props.application});
      this.getApplication();
    }
  }

  getApplication() {
    if (!this.state.application) {
      return;
    }

    this.setState({loading: true, error: null});

    ApplicationBackend.getApplication(this.state.application.owner, this.state.application.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({application: res.data, loading: false});
        } else {
          this.setState({error: res.msg, loading: false});
        }
      })
      .catch(error => {
        this.setState({error: error.toString(), loading: false});
      });
  }

  copyToClipboard = (text) => {
    copy(text);
    Setting.showMessage("success", i18next.t("general:Successfully copied"));
  };

  renderBasic() {
    const details = this.state.application?.details;
    if (!details) {
      return null;
    }

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
        <div className="grid grid-cols-2 gap-2 text-sm">
          <div><span className="text-zinc-500">{i18next.t("general:Name")}: </span>
            <span className="text-zinc-300 text-sm">{this.state.application.name}</span>
          </div>
          <div><span className="text-zinc-500">{i18next.t("general:Namespace")}: </span>
            <span className="text-zinc-300 text-sm">{details.namespace}</span>
          </div>
          <div><span className="text-zinc-500">{i18next.t("general:Status")}: </span>
            {Setting.getApplicationStatusTag(details.status)}
          </div>
          <div><span className="text-zinc-500">{i18next.t("general:Created time")}: </span>
            <span className="text-zinc-300 text-sm">{details.createdTime}</span>
          </div>
        </div>
      </div>
    );
  }

  renderMetrics() {
    const details = this.state.application?.details;
    if (!details || !details.metrics) {
      return null;
    }

    const metrics = details.metrics;

    const getProgressColor = (percentage) => {
      if (percentage < 50) {return "#52c41a";}
      if (percentage < 80) {return "#faad14";}
      return "#f5222d";
    };

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
              <Statistic title={i18next.t("system:CPU Usage")} value={metrics.cpuUsage} suffix={metrics.cpuPercentage > 0 ? `(${metrics.cpuPercentage.toFixed(1)}%)` : ""} />
              {metrics.cpuPercentage > 0 ? (
                <Progress percent={metrics.cpuPercentage} size="small" strokeColor={getProgressColor(metrics.cpuPercentage)} showInfo={false} style={{marginTop: 8}} />
              ) : (
                <div style={{height: 14, marginTop: 8}}></div>
              )}
            </div>
          </div>

          <div className="flex-1">
            <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
              <Statistic title={i18next.t("system:Memory Usage")} value={metrics.memoryUsage} suffix={metrics.memoryPercentage > 0 ? `(${metrics.memoryPercentage.toFixed(1)}%)` : ""} />
              {metrics.memoryPercentage > 0 ? (
                <Progress percent={metrics.memoryPercentage} size="small" strokeColor={getProgressColor(metrics.memoryPercentage)} showInfo={false} style={{marginTop: 8}} />
              ) : (
                <div style={{height: 14, marginTop: 8}}></div>
              )}
            </div>
          </div>

          <div className="flex-1">
            <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
              <Statistic title={i18next.t("general:Pods")} value={metrics.podCount} />
              <div style={{height: 14, marginTop: 8}}></div>
            </div>
          </div>
        </div>
      </div>
    );
  }

  renderConnections() {
    const details = this.state.application?.details;
    if (!details || !details.services || details.services.length === 0) {
      return null;
    }

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
        {details.services.map((service, index) => (
          <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">{service.name}<span className="px-2 py-0.5 bg-zinc-800 text-zinc-400 rounded text-xs">{service.type}</span></span>}
            style={{marginBottom: 12}} type="inner">

            {/* Internal Access */}
            <div style={{marginBottom: 16}}>
              <span className="text-zinc-300 text-sm">{i18next.t("machine:Private IP")}</span>
              <div style={{display: "flex", alignItems: "center"}}>
                <span className="text-zinc-300 text-sm">{service.internalHost}</span>
                <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small"
                  disabled={!service.internalHost}
                  onClick={() => this.copyToClipboard(service.internalHost)}>
                  {i18next.t("general:Copy")}
                </button>
              </div>
            </div>

            {/* External Access */}
            {service.externalHost && service.type !== "ClusterIP" && (
              <div style={{marginBottom: 16}}>
                <span className="text-zinc-300 text-sm">{i18next.t("machine:Public IP")}</span>
                <div style={{display: "flex", alignItems: "center"}}>
                  <span className="text-zinc-300 text-sm">{service.externalHost}</span>
                  <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small"
                    disabled={!service.externalHost}
                    onClick={() => this.copyToClipboard(service.externalHost)}>
                    {i18next.t("general:Copy")}
                  </button>
                </div>
              </div>
            )}

            {/* Port Mappings */}
            {service.ports && service.ports.length > 0 && (
              <div>
                <span className="text-zinc-300 text-sm">{i18next.t("container:Ports")}</span>
                {service.ports.map((port, portIndex) => (
                  <div key={portIndex} style={{marginBottom: 4}}>
                    <div style={{display: "flex", alignItems: "center"}}>
                      <span className="text-zinc-300 text-sm">
                        {port.name ? `${port.name}: ` : ""}
                        {port.port}/{port.protocol}
                        {port.nodePort && ` → ${port.nodePort}`}
                      </span>
                      {port.url && (
                        <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700">} size="small"
                          onClick={() => this.copyToClipboard(port.url)}>
                          {i18next.t("general:Copy")}
                        </button>
                      )}
                    </div>
                    {port.url && (
                      <div style={{marginTop: 4}}>
                        <span className="text-zinc-300 text-sm">
                          <a target="_blank" rel="noreferrer" href={port.url}>
                            {port.url}
                          </a>
                        </span>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    );
  }

  render() {
    if (!this.state.application) {
      return null;
    }

    if (this.state.application.status === "Not Deployed") {
      return (
        <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          <div style={{textAlign: "center", padding: "20px"}}>
            <span className="text-zinc-300 text-sm">{i18next.t("application:Not Deployed")}</span>
          </div>
        </div>
      );
    }

    if (this.state.error) {
      return (
        <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          <div style={{textAlign: "center", padding: "20px"}}>
            <span className="text-zinc-300 text-sm">{i18next.t("general:Error")}: {this.state.error}</span>
          </div>
        </div>
      );
    }

    const details = this.state.application?.details;

    return (
      <div style={{margin: "32px"}}>
        {this.renderBasic()}
        {this.renderMetrics()}
        <DeploymentTable deployments={details?.deployments} />
        {this.renderConnections()}
        <EventTable events={details?.events} />
        <CredentialsTable credentials={details?.credentials} />
      </div>
    );
  }
}

export default ApplicationViewPage;
