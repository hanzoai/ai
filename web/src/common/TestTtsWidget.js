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
import * as TtsBackend from "../backend/TtsBackend";
import {checkProvider} from "./ProviderWidget";

class TtsTestWidget extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      testButtonLoading: false,
    };
    this.audioPlayer = null;
  }

  componentDidMount() {
    if (this.props.provider && this.props.provider.category === "Text-to-Speech") {
      // Set default test content if empty
      if (this.props.provider.testContent === "") {
        const defaultContent = "Hello, this is a test for text to speech conversion.";
        this.props.provider.testContent = defaultContent;
        // Call the update function if provided
        if (this.props.onUpdateProvider) {
          this.props.onUpdateProvider("testContent", defaultContent);
        }
      }
    }
  }

  componentDidUpdate(prevProps) {
    if (prevProps.provider?.name !== this.props.provider?.name &&
        this.props.provider?.category === "Text-to-Speech") {
      // Set default test content if empty
      if (this.props.provider.testContent === "") {
        const defaultContent = "Hello, this is a test for text to speech conversion.";
        this.props.provider.testContent = defaultContent;
        // Call the update function if provided
        if (this.props.onUpdateProvider) {
          this.props.onUpdateProvider("testContent", defaultContent);
        }
      }
    }
  }

  componentWillUnmount() {
    // Clean up audio player when component unmounts
    if (this.audioPlayer) {
      this.audioPlayer.pause();
      this.audioPlayer = null;
    }
  }

  async sendTestTts(provider, originalProvider, text, owner, user) {
    await checkProvider(provider, originalProvider);
    this.setState({testButtonLoading: true});

    if (this.audioPlayer) {
      this.audioPlayer.pause();
      this.audioPlayer = null;
    }

    try {
      const providerId = `${provider.owner}/${provider.name}`;
      const audioBlob = await TtsBackend.generateTextToSpeechAudio("", providerId, "", text);

      if (audioBlob) {
        const audioUrl = URL.createObjectURL(audioBlob);
        this.audioPlayer = new Audio(audioUrl);
        if (audioBlob.type === "application/json") {
          Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
          this.setState({testButtonLoading: false});
          return;
        }

        this.audioPlayer.onended = () => {
          URL.revokeObjectURL(audioUrl);
          this.audioPlayer = null;
        };

        this.audioPlayer.onerror = (e) => {
          Setting.showMessage("error", `${i18next.t("provider:Failed to play audio")}: ${e.target.error?.message || "Unknown error"}`);
          URL.revokeObjectURL(audioUrl);
          this.setState({testButtonLoading: false});
        };

        await this.audioPlayer.play();
      }
    } catch (error) {
      Setting.showMessage("error", `${i18next.t("general:Failed to connect to server")}: ${error.message}`);
    } finally {
      this.setState({testButtonLoading: false});
    }
  }

  render() {
    const {provider, originalProvider, account, onUpdateProvider} = this.props;

    if (!provider || provider.category !== "Text-to-Speech") {
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
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200 disabled:opacity-50" disabled={!provider.testContent} style={{marginLeft: "10px", marginBottom: "5px"}> this.sendTestTts(provider, originalProvider, provider.testContent, account.owner, account.name)} >
              {i18next.t("chat:Read it out")}
            </button>
          </div>
        </div>
      </React.Fragment>
    );
  }
}

export default TtsTestWidget;
