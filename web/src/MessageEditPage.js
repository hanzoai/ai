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
import i18next from "i18next";
import * as Setting from "./Setting";
import * as MessageBackend from "./backend/MessageBackend";
import * as ChatBackend from "./backend/ChatBackend";
import * as ProviderBackend from "./backend/ProviderBackend";


class MessageEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      messageName: props.match.params.messageName,
      isNewMessage: props.location?.state?.isNewMessage || false,
      messages: [],
      message: null,
      chats: [],
      // users: [],
      chat: null,
      provider: null,
      providers: [],
    };
  }

  UNSAFE_componentWillMount() {
    this.getMessage();
    this.getMessages();
    this.getChats();
    this.getProviders();
  }

  getProviders() {
    ProviderBackend.getProviders("admin")
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            providers: res.data.filter(p => p.category === "Model"),
          });
        }
      });
  }

  getProvider(providerName) {
    if (!providerName) {
      return;
    }
    ProviderBackend.getProvider("admin", providerName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            provider: res.data,
          });
        }
      });
  }

  getChats() {
    ChatBackend.getChats(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            chats: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getChat(chatName) {
    ChatBackend.getChat("admin", chatName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            chat: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getMessage() {
    MessageBackend.getMessage("admin", this.state.messageName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            message: res.data,
          });
          this.getProvider(res.data.modelProvider);
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getMessages() {
    MessageBackend.getMessages(this.props.account.name)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            messages: res.data,
          });
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  parseMessageField(key, value) {
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateMessageField(key, value) {
    value = this.parseMessageField(key, value);

    const message = this.state.message;
    message[key] = value;
    this.setState({
      message: message,
    });
  }

  renderMessage() {
    return (
      <div className="bg-zinc-900/50 border border-zinc-800 rounded-lg p-4">
          {i18next.t("message:Edit Message")}&nbsp;&nbsp;&nbsp;&nbsp;
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitMessageEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitMessageEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewMessage && <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelMessageEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      } style={(Setting.isMobile()) ? {margin: "5px"} : {}} type="inner">
        {/* <div className="flex flex-col sm:flex-row gap-2 mt-4">*/}
        {/*  <div className="flex-1">*/}
        {/*    {Setting.getLabel(i18next.t("general:Organization"), i18next.t("general:Organization - Tooltip"))}:*/}
        {/*  </div>*/}
        {/*  <div className="flex-1">*/}
        {/*    <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.chat.organization} disabled> {this.updateChatField("organization", value);})}*/}
        {/*      options={this.state.organizations.map((organization) => Setting.getOption(organization.name, organization.name))*/}
        {/*      } />*/}
        {/*  </div>*/}
        {/* </div>*/}
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input
              value={this.state.message.name}
              onChange={(e) => {
                this.updateMessageField("name", e.target.value);
              }}
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:User"), i18next.t("general:User - Tooltip"))} :
          </div>
          <div className="flex-1">
            <Input value={this.state.message.user} onChange={e => {
              this.updateMessageField("user", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Chat"), i18next.t("general:Chat - Tooltip"))} :
          </div>
          <div className="flex-1">
            <button className="px-3 py-1.5 rounded text-xs font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.props.history.push(`/chats/${this.state.message.chat}`)} >
              {this.state.message.chat}
            </button>
            {/* <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.message.chat}> {*/}
            {/*    this.updateMessageField("chat", value);*/}
            {/*    this.getChat(value);*/}
            {/*  }}*/}
            {/*  options={this.state.chats.map((chat) =>*/}
            {/*    Setting.getOption(chat.name, chat.name)*/}
            {/*  )}*/}
            {/* />*/}
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("message:Author"), i18next.t("message:Author - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.message.author}> {
                this.updateMessageField("author", value);
              }}
              options={
                this.state.chat !== null
                  ? this.state.chat.users.map((user) =>
                    Setting.getOption(`${user}`, `${user}`)
                  )
                  : []
              }
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("provider:Model provider"), i18next.t("provider:Model provider - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.message.modelProvider}> {
                this.updateMessageField("modelProvider", value);
                this.getProvider(value);
              }}
              showSearch
              filterOption={(input, option) =>
                option.children[1].toLowerCase().includes(input.toLowerCase())
              }
            >
              {
                this.state.providers.map((provider, index) => (
                  <option key={index} value={provider.name}>
                    <img width={20} height={20} style={{marginBottom: "3px", marginRight: "10px"}} src={Setting.getProviderLogoURL({category: provider.category, type: provider.type})} alt={provider.type} />
                    {provider.name}
                  </option>
                ))
              }
            </select>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("message:Reply to"), i18next.t("message:Reply to - Tooltip"))} :
          </div>
          <div className="flex-1">
            <select className="w-full px-3 py-2 bg-zinc-800 border border-zinc-700 rounded text-sm text-white focus:outline-none focus:border-zinc-500 disabled:opacity-50" value={this.state.message.replyTo}> {
                this.updateMessageField("replyTo", value);
              }}
              options={
                this.state.messages !== null
                  ? this.state.messages.map((message) =>
                    Setting.getOption(`${message.name}`, `${message.name}`)
                  )
                  : []
              }
            />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Reasoning text"), i18next.t("general:Reasoning text - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="text-zinc-300 text-sm"> {
              this.updateMessageField("reasonText", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Text"), i18next.t("general:Text - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="text-zinc-300 text-sm"> {
              this.updateMessageField("text", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("message:Error text"), i18next.t("message:Error text - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="text-zinc-300 text-sm"> {
              this.updateMessageField("errorText", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("message:Comment"), i18next.t("message:Comment - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="text-zinc-300 text-sm"> {
              if (e.target.value !== "") {
                this.updateMessageField("needNotify", true);
              } else {
                this.updateMessageField("needNotify", false);
              }

              this.updateMessageField("comment", e.target.value);
            }} />
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("message:Need notify"), i18next.t("message:Need notify - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="px-2 py-0.5 rounded text-xs " + (this.state.message.needNotify ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{this.state.message.needNotify ? "ON" : "OFF"}</span>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Is deleted"), i18next.t("general:Is deleted - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="px-2 py-0.5 rounded text-xs " + (this.state.message.isDeleted ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{this.state.message.isDeleted ? "ON" : "OFF"}</span>
          </div>
        </div>
        <div className="flex flex-col sm:flex-row gap-2 mt-4">
          <div className="flex-1">
            {Setting.getLabel(i18next.t("general:Is alerted"), i18next.t("general:Is alerted - Tooltip"))} :
          </div>
          <div className="flex-1">
            <span className="px-2 py-0.5 rounded text-xs " + (this.state.message.isAlerted ? "bg-green-500/20 text-green-400" : "bg-zinc-800 text-zinc-500")">{this.state.message.isAlerted ? "ON" : "OFF"}</span>
          </div>
        </div>
      </div>
    );
  }

  submitMessageEdit(exitAfterSave) {
    const message = Setting.deepCopy(this.state.message);
    MessageBackend.updateMessage(this.state.message.owner, this.state.messageName, message)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              messageName: this.state.message.name,
              isNewMessage: false,
            });
            if (exitAfterSave) {
              this.props.history.push("/messages");
            } else {
              this.props.history.push(`/messages/${this.state.message.name}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
            this.updateMessageField("name", this.state.messageName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch((error) => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  render() {
    return (
      <div>
        {this.state.message !== null ? this.renderMessage() : null}
        <div style={{marginTop: "20px", marginLeft: "40px"}}>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700"> this.submitMessageEdit(false)}>{i18next.t("general:Save")}</button>
          <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-white text-black hover:bg-zinc-200" style={{marginLeft: "20px"}> this.submitMessageEdit(true)}>{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewMessage && <button className="px-6 py-2 rounded text-sm font-medium transition-colors bg-zinc-800 text-zinc-300 hover:bg-zinc-700" style={{marginLeft: "20px"}> this.cancelMessageEdit()}>{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
  cancelMessageEdit() {
    if (this.state.isNewMessage) {
      MessageBackend.deleteMessage(this.state.message)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/messages");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/messages");
    }
  }

}

export default MessageEditPage;
