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
import * as AssetBackend from "./backend/AssetBackend";
import * as ProviderBackend from "./backend/ProviderBackend";
import * as ScanBackend from "./backend/ScanBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import {JsonCodeMirrorEditor} from "./common/JsonCodeMirrorWidget";
import ScanTable from "./common/ScanTable";

class AssetEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      assetName: props.match.params.assetName,
      asset: null,
      providers: [],
      scans: [],
      loadingScans: false,
      mode: props.location.mode !== undefined ? props.location.mode : "edit",
    };
  }

  UNSAFE_componentWillMount() {
    this.getAsset();
    this.getProviders();
    this.getScans();
  }

  getProviders() {
    ProviderBackend.getProviders(this.props.account.owner)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            providers: res.data || [],
          });
        } else {
          Setting.showMessage("error", res.msg);
        }
      });
  }

  getScans() {
    this.setState({loadingScans: true});
    ScanBackend.getScansByAsset("admin", this.state.assetName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            scans: res.data || [],
            loadingScans: false,
          });
        } else {
          this.setState({
            scans: [],
            loadingScans: false,
          });
        }
      })
      .catch(() => {
        this.setState({
          scans: [],
          loadingScans: false,
        });
      });
  }

  getAsset() {
    AssetBackend.getAsset("admin", this.state.assetName)
      .then((res) => {
        if (res.status === "ok") {
          const asset = res.data;
          // Format JSON properties with 2-space indentation
          if (asset.properties) {
            asset.properties = Setting.formatJsonString(asset.properties);
          }
          this.setState({
            asset: asset,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseAssetField(key, value) {
    return value;
  }

  updateAssetField(key, value) {
    value = this.parseAssetField(key, value);
    const asset = this.state.asset;
    asset[key] = value;
    this.setState({
      asset: asset,
    });
  }

  submitAssetEdit(exitAfterSave) {
    const asset = Setting.deepCopy(this.state.asset);
    AssetBackend.updateAsset(this.state.asset.owner, this.state.assetName, asset)
      .then((res) => {
        if (res.status === "ok") {
          Setting.showMessage("success", i18next.t("general:Successfully saved"));
          this.setState({
            assetName: this.state.asset.name,
          });

          if (exitAfterSave) {
            this.props.history.push("/assets");
          } else {
            this.props.history.push(`/assets/${this.state.asset.name}`);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  deleteAsset() {
    AssetBackend.deleteAsset(this.state.asset)
      .then((res) => {
        if (res.status === "ok") {
          this.props.history.push("/assets");
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to delete")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error}`);
      });
  }

  getProviderLogo(providerName) {
    const provider = this.state.providers.find(p => p.name === providerName);
    if (!provider) {
      return null;
    }

    const otherProviderInfo = Setting.getOtherProviderInfo();
    if (!otherProviderInfo[provider.category] || !otherProviderInfo[provider.category][provider.type]) {
      return null;
    }

    return otherProviderInfo[provider.category][provider.type].logo;
  }

  getTypeIcon(typeName) {
    const typeIcons = Setting.getAssetTypeIcons();
    return typeIcons[typeName] || null;
  }

  renderAsset() {
    const providerLogo = this.getProviderLogo(this.state.asset.provider);
    const typeIcon = this.getTypeIcon(this.state.asset.type);

    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {this.state.mode === "add" ? i18next.t("asset:New Asset") : i18next.t("asset:Edit Asset")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitAssetEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitAssetEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteAsset()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      } style={{marginLeft: "5px"}} type="inner">
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Organization"), i18next.t("general:Organization - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.asset.owner} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.asset.name} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.asset.displayName} onChange={e => {
              this.updateAssetField("displayName", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Provider"), i18next.t("general:Provider - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.asset.provider} disabled /> :
                  null
              }
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:ID"), i18next.t("general:ID - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.asset.id} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Type"), i18next.t("general:Type - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.asset.type} disabled /> :
                  null
              }
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Region"), i18next.t("general:Region - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.asset.region} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Zone"), i18next.t("general:Zone - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.asset.zone} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:State"), i18next.t("general:State - Tooltip"))} :
          </div>
          <div className="flex-1">
            <input className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white placeholder-zinc-500 focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.asset.state} disabled />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Tag"), i18next.t("general:Tag - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.asset.tag} onChange={e => {
              this.updateAssetField("tag", e.target.value);
            }} />
          </div>
        </div>
        {this.state.asset.type === "Virtual Machine" ? (
          <>
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("general:Username"), i18next.t("general:Username - Tooltip"))} :
              </div>
              <div className="flex-1">
                <Input value={this.state.asset.username} onChange={e => {
                  this.updateAssetField("username", e.target.value);
                }} />
              </div>
            </div>
            <div className="flex flex-col sm:flex-row gap-2 mt-4">
              <div className="flex-1">
                {Setting.getLabel(i18next.t("general:Password"), i18next.t("general:Password - Tooltip"))} :
              </div>
              <div className="flex-1">
                <Input.Password value={this.state.asset.password} onChange={e => {
                  this.updateAssetField("password", e.target.value);
                }} />
              </div>
            </div>
          </>
        ) : null}
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("asset:Properties"), i18next.t("asset:Properties - Tooltip"))} :
          </div>
          <div className="flex-1">
            <JsonCodeMirrorEditor
              value={this.state.asset.properties || ""}
              onChange={(editor, data, value) => {
                this.updateAssetField("properties", value);
              }}
              editable={true}
              height="500px"
            />
          </div>
        </div>
      </div>
    );
  }

  render() {
    return (
      <div>
        {
          this.state.asset !== null ? this.renderAsset() : null
        }
        {this.state.asset !== null && (
          <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
            {this.state.loadingScans ? (
              <div style={{textAlign: "center", padding: "40px"}}>
                {i18next.t("general:Loading")}...
              </div>
            ) : this.state.scans.length > 0 ? (
              <ScanTable scans={this.state.scans} providers={this.state.providers} showAsset={false} />
            ) : (
              <div style={{textAlign: "center", padding: "40px", color: "#999"}}>
                {i18next.t("scan:No scans found for this asset")}
              </div>
            )}
          </div>
        )}
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitAssetEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitAssetEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.mode === "add" ? <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.deleteAsset()}>{i18next.t("general:Cancel")}</button> : null}
        </div>
      </div>
    );
  }
}

export default AssetEditPage;
