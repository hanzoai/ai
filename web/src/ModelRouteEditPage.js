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

import React from "react";
import {Button, Card, Col, Input, InputNumber, Row, Switch} from "antd";
import * as ModelRouteBackend from "./backend/ModelRouteBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import moment from "moment";

class ModelRouteEditPage extends React.Component {
  constructor(props) {
    super(props);
    const isNew = props.match.path === "/model-routes/new";
    this.state = {
      owner: isNew ? "admin" : props.match.params.owner,
      modelName: isNew ? "" : decodeURIComponent(props.match.params.modelName),
      route: null,
      isNew: isNew,
    };
  }

  UNSAFE_componentWillMount() {
    if (this.state.isNew) {
      this.setState({
        route: {
          owner: "admin",
          modelName: "",
          createdTime: moment().format(),
          updatedTime: moment().format(),
          provider: "",
          upstream: "",
          fallback1Provider: "",
          fallback1Upstream: "",
          fallback2Provider: "",
          fallback2Upstream: "",
          ownedBy: "",
          premium: false,
          hidden: false,
          inputPricePerMillion: 0,
          outputPricePerMillion: 0,
          enabled: true,
        },
      });
    } else {
      this.getModelRoute();
    }
  }

  getModelRoute() {
    ModelRouteBackend.getModelRoute(this.state.owner, this.state.modelName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            route: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  updateRouteField(key, value) {
    const route = Setting.deepCopy(this.state.route);
    route[key] = value;
    this.setState({route: route});
  }

  submitModelRoute() {
    if (this.state.isNew) {
      ModelRouteBackend.addModelRoute(this.state.route)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Successfully added"));
            this.props.history.push("/model-routes");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${error}`);
        });
    } else {
      ModelRouteBackend.updateModelRoute(this.state.owner, this.state.modelName, this.state.route)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              owner: this.state.route.owner,
              modelName: this.state.route.modelName,
            });
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
        });
    }
  }

  renderRoute() {
    const route = this.state.route;
    if (!route) {
      return null;
    }

    return (
      <Card size="small" title={
        <div>
          {this.state.isNew ? "New Model Route" : `Model Route: ${route.owner}/${route.modelName}`}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <Row style={{marginTop: "10px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel(i18next.t("general:Owner"), "The organization that owns this route")}
          </Col>
          <Col span={22} >
            <Input value={route.owner} onChange={e => this.updateRouteField("owner", e.target.value)} disabled={!this.state.isNew} />
          </Col>
        </Row>
        <Row style={{marginTop: "20px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel(i18next.t("general:Model Name"), "The user-facing model name (e.g. claude-sonnet-4-6)")}
          </Col>
          <Col span={22} >
            <Input value={route.modelName} onChange={e => this.updateRouteField("modelName", e.target.value)} disabled={!this.state.isNew} />
          </Col>
        </Row>
        <Row style={{marginTop: "20px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel(i18next.t("general:Provider"), "Primary provider name (must match a provider in the Providers list)")}
          </Col>
          <Col span={22} >
            <Input value={route.provider} onChange={e => this.updateRouteField("provider", e.target.value)} />
          </Col>
        </Row>
        <Row style={{marginTop: "20px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Upstream", "Model ID sent to the upstream provider API")}
          </Col>
          <Col span={22} >
            <Input value={route.upstream} onChange={e => this.updateRouteField("upstream", e.target.value)} />
          </Col>
        </Row>
        <Row style={{marginTop: "20px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Owned By", "Override for owned_by in /api/models listing")}
          </Col>
          <Col span={22} >
            <Input value={route.ownedBy} onChange={e => this.updateRouteField("ownedBy", e.target.value)} />
          </Col>
        </Row>
        <Row style={{marginTop: "20px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Fallback 1 Provider", "First fallback provider")}
          </Col>
          <Col span={10} >
            <Input value={route.fallback1Provider} onChange={e => this.updateRouteField("fallback1Provider", e.target.value)} />
          </Col>
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Fallback 1 Upstream", "First fallback upstream model")}
          </Col>
          <Col span={10} >
            <Input value={route.fallback1Upstream} onChange={e => this.updateRouteField("fallback1Upstream", e.target.value)} />
          </Col>
        </Row>
        <Row style={{marginTop: "20px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Fallback 2 Provider", "Second fallback provider")}
          </Col>
          <Col span={10} >
            <Input value={route.fallback2Provider} onChange={e => this.updateRouteField("fallback2Provider", e.target.value)} />
          </Col>
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Fallback 2 Upstream", "Second fallback upstream model")}
          </Col>
          <Col span={10} >
            <Input value={route.fallback2Upstream} onChange={e => this.updateRouteField("fallback2Upstream", e.target.value)} />
          </Col>
        </Row>
        <Row style={{marginTop: "20px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Premium", "Requires paid balance (beyond starter credit)")}
          </Col>
          <Col span={4} >
            <Switch checked={route.premium} onChange={checked => this.updateRouteField("premium", checked)} />
          </Col>
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Hidden", "Excluded from /api/models listing but still callable")}
          </Col>
          <Col span={4} >
            <Switch checked={route.hidden} onChange={checked => this.updateRouteField("hidden", checked)} />
          </Col>
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel(i18next.t("general:Enabled"), "Whether this route is active")}
          </Col>
          <Col span={4} >
            <Switch checked={route.enabled} onChange={checked => this.updateRouteField("enabled", checked)} />
          </Col>
        </Row>
        <Row style={{marginTop: "20px"}} >
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Input Price ($/M)", "Custom input price per million tokens (0 = use default)")}
          </Col>
          <Col span={10} >
            <InputNumber value={route.inputPricePerMillion} min={0} step={0.01} style={{width: "200px"}}
              onChange={value => this.updateRouteField("inputPricePerMillion", value || 0)} />
          </Col>
          <Col style={{marginTop: "5px"}} span={2}>
            {Setting.getLabel("Output Price ($/M)", "Custom output price per million tokens (0 = use default)")}
          </Col>
          <Col span={10} >
            <InputNumber value={route.outputPricePerMillion} min={0} step={0.01} style={{width: "200px"}}
              onChange={value => this.updateRouteField("outputPricePerMillion", value || 0)} />
          </Col>
        </Row>
      </Card>
    );
  }

  render() {
    return (
      <div>
        {this.renderRoute()}
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <Button size="large" onClick={() => this.props.history.push("/model-routes")}>{i18next.t("general:Cancel")}</Button>
          &nbsp;&nbsp;&nbsp;
          <Button type="primary" size="large" onClick={() => this.submitModelRoute()}>{i18next.t("general:Save")}</Button>
        </div>
      </div>
    );
  }
}

export default ModelRouteEditPage;
