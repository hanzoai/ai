// Copyright 2024 The Hanzo AI Authors. All Rights Reserved.
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
import {Card, Spin} from "antd";
import * as VmBackend from "./backend/VmBackend";
import i18next from "i18next";

class VmPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      account: props.account,
      loading: true,
      dashboardUrl: null,
      error: null,
    };
  }

  UNSAFE_componentWillMount() {
    this.getVmDashboardUrl();
  }

  getVmDashboardUrl() {
    VmBackend.getVmDashboardUrl()
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            dashboardUrl: res.data,
            loading: false,
          });
        } else {
          this.setState({
            error: res.msg,
            loading: false,
          });
        }
      })
      .catch((error) => {
        this.setState({
          error: error.message,
          loading: false,
        });
      });
  }

  render() {
    if (this.state.loading) {
      return (
        <div style={{display: "flex", justifyContent: "center", alignItems: "center", height: "60vh"}}>
          <Spin size="large" tip={i18next.t("general:Loading...")} />
        </div>
      );
    }

    if (this.state.error) {
      return (
        <Card title={i18next.t("general:Virtual Machines")}>
          <div style={{textAlign: "center", padding: "40px"}}>
            <p>{i18next.t("general:Failed to load VM dashboard")}: {this.state.error}</p>
            <p style={{color: "#999", fontSize: "12px"}}>
              {i18next.t("general:Configure VM_DASHBOARD_URL environment variable to set the VM control plane URL")}
            </p>
          </div>
        </Card>
      );
    }

    return (
      <div style={{width: "100%", height: "calc(100vh - 140px)"}}>
        <iframe
          src={this.state.dashboardUrl}
          style={{
            width: "100%",
            height: "100%",
            border: "none",
            borderRadius: "8px",
          }}
          title="Hanzo VM Dashboard"
          allow="clipboard-write"
        />
      </div>
    );
  }
}

export default VmPage;
