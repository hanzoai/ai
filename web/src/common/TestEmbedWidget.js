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
import * as Setting from "../Setting";
import i18next from "i18next";
import * as VectorBackend from "../backend/VectorBackend";
import {checkProvider} from "./ProviderWidget";


class TestEmbedWidget extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      testButtonLoading: false,
      embeddingResult: null,
    };
  }

  componentDidMount() {
    if (this.props.provider && this.props.provider.category === "Embedding") {
      // Set default test content if empty
      if (this.props.provider.testContent === "") {
        const defaultContent = "This is a sample text for embedding generation.";
        this.props.provider.testContent = defaultContent;
        if (this.props.onUpdateProvider) {
          this.props.onUpdateProvider("testContent", defaultContent);
        }
      }
    }
  }

  async sendTestEmbedding(provider, originalProvider, text) {
    await checkProvider(provider, originalProvider);
    this.setState({testButtonLoading: true, embeddingResult: null});

    try {
      const testVectorName = `test_${provider.name}`;

      const testVector = {
        owner: "admin",
        name: testVectorName,
        provider: provider.name,
        text: "",
      };

      await VectorBackend.deleteVector(testVector);

      // Create new empty vector
      const addResult = await VectorBackend.addVector(testVector);
      if (addResult.status !== "ok") {
        Setting.showMessage("error", `${i18next.t("general:Failed to add")}: ${addResult.msg}`);
        return;
      }

      testVector.text = text;
      const updateResult = await VectorBackend.updateVector("admin", testVectorName, testVector);
      if (updateResult.status !== "ok") {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${updateResult.msg}`);
        return;
      }

      // Get generated vector data
      const vectorResult = await VectorBackend.getVector("admin", testVectorName);
      if (vectorResult.status === "ok" && vectorResult.data && vectorResult.data.data) {
        this.setState({
          embeddingResult: vectorResult.data.data,
        });
      } else {
        Setting.showMessage("error", i18next.t("general:Failed to get"));
      }
    } catch (error) {
      Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error.message}`);
    } finally {
      this.setState({testButtonLoading: false});
    }
  }

  render() {
    const {provider, originalProvider, onUpdateProvider} = this.props;

    if (!provider || provider.category !== "Embedding") {
      return null;
    }

    return (
      <React.Fragment>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("provider:Provider test"), i18next.t("provider:Provider test - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input.TextArea rows={1} autoSize={{minRows: 1, maxRows: 5}} value={provider.testContent} onChange={e => {onUpdateProvider("testContent", e.target.value);}} />
          </div>
          <div className="flex-1">
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200 disabled:opacity-50" disabled={!provider.testContent} style={{marginLeft: "10px", marginBottom: "5px"}> this.sendTestEmbedding(provider, originalProvider, provider.testContent, this.props.account)}>
              {i18next.t("general:Refresh Vectors")}
            </button>
          </div>
        </div>
        {this.state.embeddingResult && (
          <div className="flex flex-col sm:flex-row gap-2 mt-4">
            <div className="flex-1"></div>
            <div className="flex-1">
              <div style={{border: "1px solid #d9d9d9", borderRadius: "6px", padding: "10px", backgroundColor: "#fafafa"}}>
                <div><strong>{i18next.t("general:Data")}:</strong></div>
                <span className="text-zinc-300 text-sm">
              </div>
            </div>
          </div>
        )}
      </React.Fragment>
    );
  }
}

export default TestEmbedWidget;
