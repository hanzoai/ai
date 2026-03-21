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
import {Check, ChevronDown, ChevronRight, LayoutGrid, Pencil, Plus, Save, Trash2, X} from "lucide-react";
import i18next from "i18next";

class ChatMenu extends React.Component {
  constructor(props) {
    super(props);

    const items = this.chatsToItems(this.props.chats);
    const selectedKey = this.getSelectedKeyOfCurrentChat(this.props.chats, this.props.chatName);
    const openKeys = items.map((item) => item.key);

    this.state = {
      openKeys: openKeys,
      selectedKeys: [selectedKey],
      editChat: false,
      editChatName: "",
      confirmDelete: null,
    };
  }

  chatsToItems(chats) {
    const categories = {};
    chats.forEach((chat) => {
      if (chat.isHidden === true) {
        return;
      }
      if (!categories[chat.category]) {
        categories[chat.category] = [];
      }
      categories[chat.category].push(chat);
    });

    return Object.keys(categories).map((category, index) => ({
      key: `${index}`,
      label: category,
      children: categories[category].map((chat, chatIndex) => ({
        key: `${index}-${chatIndex}`,
        index: chats.indexOf(chat),
        chat,
      })),
    }));
  }

  onSelect = (categoryIndex, chatIndex, globalIndex) => {
    this.setState({
      selectedKeys: [`${categoryIndex}-${chatIndex}`],
      editChat: false,
      confirmDelete: null,
    });

    if (this.props.onSelectChat) {
      this.props.onSelectChat(globalIndex);
    }
  };

  setSelectedKeyToNewChat(chats) {
    const items = this.chatsToItems(chats);
    const openKeys = items.map((item) => item.key);
    this.setState({
      openKeys: openKeys,
      selectedKeys: ["0-0"],
    });
  }

  getSelectedKeyOfCurrentChat(chats, chatName) {
    if (!chatName) {return null;}
    const items = this.chatsToItems(chats);
    const chat = chats.find(chat => chat.name === chatName);
    let selectedKey = null;

    for (let categoryIndex = 0; categoryIndex < items.length; categoryIndex++) {
      const category = items[categoryIndex];
      for (let chatIndex = 0; chatIndex < category.children.length; chatIndex++) {
        if (category.children[chatIndex].index === chats.indexOf(chat)) {
          selectedKey = `${categoryIndex}-${chatIndex}`;
          break;
        }
      }
      if (selectedKey) {break;}
    }
    return selectedKey === null ? "0-0" : selectedKey;
  }

  toggleCategory = (key) => {
    this.setState(prev => ({
      openKeys: prev.openKeys.includes(key)
        ? prev.openKeys.filter(k => k !== key)
        : [...prev.openKeys, key],
    }));
  };

  renderAddChatButton(stores = [], currentStoreName = null) {
    if (!stores) {stores = [];}

    const defaultStore = stores.find(store => store.isDefault);
    let hasChildStores = false;

    if (currentStoreName) {
      const currentStore = stores.find(store => store.name === currentStoreName);
      if (currentStore) {
        stores = [];
        hasChildStores = false;
      }
    } else if (defaultStore) {
      if (!defaultStore.childStores || defaultStore.childStores.length === 0) {
        stores = [];
      } else {
        stores = stores.filter(store => defaultStore.childStores.includes(store.name));
        hasChildStores = true;
      }
    }

    return (
      <button
        className="w-[calc(100%-8px)] h-10 mx-1 my-1 rounded-md border border-border text-foreground flex items-center justify-center gap-2 text-sm hover:border-primary/50 hover:text-primary transition-colors"
        onClick={() => {
          if (currentStoreName) {
            const currentStore = this.props.stores.find(store => store.name === currentStoreName);
            this.props.onAddChat(currentStore);
          } else if (!hasChildStores) {
            this.props.onAddChat(defaultStore);
          }
        }}
      >
        <Plus className="w-4 h-4" />
        {i18next.t("chat:New Chat")}
      </button>
    );
  }

  render() {
    const items = this.chatsToItems(this.props.chats);

    return (
      <div className="h-full flex flex-col">
        {this.renderAddChatButton(this.props.stores, this.props.currentStoreName)}
        <div className="flex-1 overflow-y-auto mr-1" style={{maxHeight: "calc(100vh - 140px - 40px - 8px)"}}>
          {items.map((category) => {
            const isOpen = this.state.openKeys.includes(category.key);
            return (
              <div key={category.key}>
                {/* Category header */}
                <button
                  onClick={() => this.toggleCategory(category.key)}
                  className="w-full flex items-center gap-2 px-3 py-2 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors"
                >
                  {isOpen ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
                  <LayoutGrid className="w-3 h-3" />
                  {category.label}
                </button>

                {/* Chat items */}
                {isOpen && category.children.map((item) => {
                  const isSelected = this.state.selectedKeys.includes(item.key);
                  const isEditing = isSelected && this.state.editChat;
                  const isConfirmingDelete = this.state.confirmDelete === item.key;

                  return (
                    <div
                      key={item.key}
                      onClick={() => this.onSelect(category.key, item.key.split("-")[1], item.index)}
                      className={`group mx-1 px-3 py-2 rounded-md cursor-pointer text-sm flex items-center justify-between transition-colors ${
                        isSelected ? "bg-accent text-accent-foreground" : "text-foreground/80 hover:bg-accent/50"
                      }`}
                    >
                      {isEditing ? (
                        <div className="flex items-center gap-1 w-full" onClick={(e) => e.stopPropagation()}>
                          <input
                            className="flex-1 bg-background border border-border rounded px-2 py-1 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
                            value={this.state.editChatName}
                            onChange={(e) => this.setState({editChatName: e.target.value})}
                            onBlur={() => {
                              this.props.onUpdateChatName(item.index, this.state.editChatName);
                              this.setState({editChat: false});
                            }}
                            onKeyDown={(e) => {
                              if (e.key === "Enter") {
                                this.props.onUpdateChatName(item.index, this.state.editChatName);
                                this.setState({editChat: false});
                              }
                            }}
                            autoFocus
                          />
                          <button
                            className="p-1 text-muted-foreground hover:text-foreground"
                            onClick={() => {
                              this.props.onUpdateChatName(item.index, this.state.editChatName);
                              this.setState({editChat: false});
                            }}
                          >
                            <Save className="w-3.5 h-3.5" />
                          </button>
                          <button
                            className="p-1 text-muted-foreground hover:text-foreground"
                            onClick={() => this.setState({editChat: false})}
                          >
                            <X className="w-3.5 h-3.5" />
                          </button>
                        </div>
                      ) : (
                        <>
                          <span className="truncate flex-1" title={item.chat.displayName}>
                            {item.chat.displayName}
                          </span>
                          {isSelected && (
                            <div className="flex items-center gap-0.5 ml-1" onClick={(e) => e.stopPropagation()}>
                              <button
                                className="p-1 text-muted-foreground hover:text-foreground rounded transition-colors"
                                onClick={() => this.setState({
                                  editChatName: this.props.chats[item.index].displayName,
                                  editChat: true,
                                })}
                              >
                                <Pencil className="w-3 h-3" />
                              </button>
                              {isConfirmingDelete ? (
                                <div className="flex items-center gap-0.5">
                                  <button
                                    className="p-1 text-destructive hover:text-destructive/80 rounded transition-colors"
                                    onClick={() => {
                                      this.props.onDeleteChat(item.index);
                                      this.setState({confirmDelete: null});
                                    }}
                                  >
                                    <Check className="w-3 h-3" />
                                  </button>
                                  <button
                                    className="p-1 text-muted-foreground hover:text-foreground rounded transition-colors"
                                    onClick={() => this.setState({confirmDelete: null})}
                                  >
                                    <X className="w-3 h-3" />
                                  </button>
                                </div>
                              ) : (
                                <button
                                  className="p-1 text-muted-foreground hover:text-destructive rounded transition-colors"
                                  onClick={() => this.setState({confirmDelete: item.key})}
                                >
                                  <Trash2 className="w-3 h-3" />
                                </button>
                              )}
                            </div>
                          )}
                        </>
                      )}
                    </div>
                  );
                })}
              </div>
            );
          })}
        </div>
      </div>
    );
  }
}

export default ChatMenu;
