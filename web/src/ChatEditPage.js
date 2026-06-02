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
import * as ChatBackend from "./backend/ChatBackend";
import * as ProviderBackend from "./backend/ProviderBackend";
import * as Setting from "./Setting";
import i18next from "i18next";
import ChatBox from "./ChatBox";
import {renderText} from "./ChatMessageRender";
import * as MessageBackend from "./backend/MessageBackend";

class ChatEditPage extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
      chatName: props.match.params.chatName,
      chat: null,
      messages: null,
      provider: null,
      providers: [],
      isNewChat: props.location?.state?.isNewChat || false,
    };
  }

  UNSAFE_componentWillMount() {
    this.getChat();
    this.getMessages(this.state.chatName);
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
    if (!providerName) {return;}
    ProviderBackend.getProvider("admin", providerName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            provider: res.data,
          });
        }
      });
  }

  getChat() {
    ChatBackend.getChat("admin", this.state.chatName)
      .then((res) => {
        if (res.status === "ok") {
          this.setState({
            chat: res.data,
          });
          this.getProvider(res.data.modelProvider);
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }

  getMessages(chatName) {
    MessageBackend.getChatMessages("admin", chatName)
      .then((res) => {
        res.data.map((message) => {
          message.html = renderText(message.text);
        });
        this.setState({
          messages: res.data,
        });
      });
  }

  parseChatField(key, value) {
    if ([""].includes(key)) {
      value = Setting.myParseInt(value);
    }
    return value;
  }

  updateChatField(key, value) {
    value = this.parseChatField(key, value);
    const chat = this.state.chat;
    chat[key] = value;
    this.setState({chat: chat});
  }

  renderFormRow(label, tooltip, content) {
    return (
      <div className="flex items-start gap-4 mt-5">
        <label className="w-40 shrink-0 pt-1 text-sm text-muted-foreground text-right">
          {Setting.getLabel(label, tooltip)} :
        </label>
        <div className="flex-1">{content}</div>
      </div>
    );
  }

  renderChat() {
    return (
      <div className="rounded-lg border border-border bg-card p-6">
        <div className="flex items-center gap-4 mb-6">
          <h2 className="text-lg font-semibold text-foreground">{i18next.t("chat:Edit Chat")}</h2>
          <button onClick={() => this.submitChatEdit(false)} className="rounded-md border border-border px-3 py-1.5 text-sm text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Save")}</button>
          <button onClick={() => this.submitChatEdit(true)} className="rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewChat && <button onClick={() => this.cancelChatEdit()} className="rounded-md border border-border px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Cancel")}</button>}
        </div>

        {this.renderFormRow(i18next.t("general:Name"), i18next.t("general:Name - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.name} onChange={e => this.updateChatField("name", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("general:Display name"), i18next.t("general:Display name - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.displayName} onChange={e => this.updateChatField("displayName", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("general:Type"), i18next.t("general:Type - Tooltip"),
          <select className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.type} onChange={e => this.updateChatField("type", e.target.value)}>
            <option value="Single">{i18next.t("chat:Single")}</option>
            <option value="Group">{i18next.t("chat:Group")}</option>
            <option value="AI">{i18next.t("chat:AI")}</option>
          </select>
        )}
        {this.renderFormRow(i18next.t("general:Store"), i18next.t("general:Store - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.store} onChange={e => this.updateChatField("store", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("provider:Model provider"), i18next.t("provider:Model provider - Tooltip"),
          <select className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.modelProvider} onChange={e => { this.updateChatField("modelProvider", e.target.value); this.getProvider(e.target.value); }}>
            {this.state.providers.map((provider, index) => (
              <option key={index} value={provider.name}>{provider.name}</option>
            ))}
          </select>
        )}
        {this.renderFormRow(i18next.t("general:Category"), i18next.t("provider:Category - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.category} onChange={e => this.updateChatField("category", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("general:User"), i18next.t("general:User - Tooltip"),
          <input className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.user} onChange={e => this.updateChatField("user", e.target.value)} />
        )}
        {this.renderFormRow(i18next.t("chat:User1"), i18next.t("chat:User1 - Tooltip"),
          <select className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.user1} onChange={e => this.updateChatField("user1", e.target.value)}>
            {this.state.chat.users.map((user) => (
              <option key={user} value={user}>{user}</option>
            ))}
          </select>
        )}
        {this.renderFormRow(i18next.t("chat:User2"), i18next.t("chat:User2 - Tooltip"),
          <select className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring" value={this.state.chat.user2} onChange={e => this.updateChatField("user2", e.target.value)}>
            <option value="">None</option>
            {this.state.chat.users.map((user) => (
              <option key={user} value={user}>{user}</option>
            ))}
          </select>
        )}
        {this.renderFormRow(i18next.t("general:Is deleted"), i18next.t("general:Is deleted - Tooltip"),
          <input type="checkbox" checked={this.state.chat.isDeleted} onChange={e => this.updateChatField("isDeleted", e.target.checked)} className="w-4 h-4 rounded border-border accent-primary" />
        )}
        {this.renderFormRow(i18next.t("general:Messages"), i18next.t("general:Messages - Tooltip"),
          <div className="w-1/2 h-[800px]">
            <ChatBox disableInput={true} hideInput={true} messages={this.state.messages} sendMessage={null} account={this.props.account} />
          </div>
        )}
      </div>
    );
  }

  submitChatEdit(exitAfterSave) {
    const chat = Setting.deepCopy(this.state.chat);
    ChatBackend.updateChat(this.state.chat.owner, this.state.chatName, chat)
      .then((res) => {
        if (res.status === "ok") {
          if (res.data) {
            Setting.showMessage("success", i18next.t("general:Successfully saved"));
            this.setState({
              chatName: this.state.chat.name,
              isNewChat: false,
            });

            if (exitAfterSave) {
              this.props.history.push("/chats");
            } else {
              this.props.history.push(`/chats/${this.state.chat.name}`);
            }
          } else {
            Setting.showMessage("error", i18next.t("general:Failed to connect to server"));
            this.updateChatField("name", this.state.chatName);
          }
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${res.msg}`);
        }
      })
      .catch(error => {
        Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error}`);
      });
  }

  cancelChatEdit() {
    if (this.state.isNewChat) {
      ChatBackend.deleteChat(this.state.chat)
        .then((res) => {
          if (res.status === "ok") {
            Setting.showMessage("success", i18next.t("general:Cancelled successfully"));
            this.props.history.push("/chats");
          } else {
            Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${res.msg}`);
          }
        })
        .catch(error => {
          Setting.showMessage("error", `${i18next.t("general:Failed to cancel")}: ${error}`);
        });
    } else {
      this.props.history.push("/chats");
    }
  }

  render() {
    return (
      <div>
        {this.state.chat !== null ? this.renderChat() : null}
        <div className="mt-5 ml-10 flex gap-4">
          <button onClick={() => this.submitChatEdit(false)} className="rounded-md border border-border px-5 py-2 text-sm text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Save")}</button>
          <button onClick={() => this.submitChatEdit(true)} className="rounded-md bg-primary px-5 py-2 text-sm text-primary-foreground hover:bg-primary/90 transition-colors">{i18next.t("general:Save & Exit")}</button>
          {this.state.isNewChat && <button onClick={() => this.cancelChatEdit()} className="rounded-md border border-border px-5 py-2 text-sm text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors">{i18next.t("general:Cancel")}</button>}
        </div>
      </div>
    );
  }
}

export default ChatEditPage;
