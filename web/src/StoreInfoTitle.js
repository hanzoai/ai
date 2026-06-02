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

import React, {useEffect, useMemo, useRef, useState} from "react";
import {Minus, Plus} from "lucide-react";
import * as Setting from "./Setting";
import * as ProviderBackend from "./backend/ProviderBackend";
import * as ChatBackend from "./backend/ChatBackend";
import i18next from "i18next";

const StoreInfoTitle = (props) => {
  const {chat, stores, onChatUpdated, onStoreChange, autoRead, onUpdateAutoRead, account, paneCount = 1, onPaneCountChange, showPaneControls = false} = props;

  const [modelProviders, setModelProviders] = useState([]);
  const [selectedStore, setSelectedStore] = useState(null);
  const [selectedProvider, setSelectedProvider] = useState(null);
  const [isUpdating, setIsUpdating] = useState(false);
  const [isMobile, setIsMobile] = useState(false);
  const [defaultStore, setDefaultStore] = useState(null);

  const storeRef = useRef();
  const providerRef = useRef();
  const chatRef = useRef();

  useEffect(() => {
    if (stores) {
      const foundDefaultStore = stores.find(store => store.isDefault);
      setDefaultStore(foundDefaultStore);
    }
  }, [stores]);

  const filteredStores = useMemo(() => {
    if (!stores || !defaultStore) {return [];}
    if (paneCount > 1) {return stores;}
    if (defaultStore.childStores && defaultStore.childStores.length > 0) {
      const childStoreNames = new Set(defaultStore.childStores);
      return stores.filter(store => childStoreNames.has(store.name));
    }
    return [];
  }, [stores, defaultStore, paneCount]);

  const canManagePanes = useMemo(() => {
    return Setting.isLocalAdminUser(account);
  }, [account]);

  useEffect(() => {
    const checkIsMobile = () => setIsMobile(window.innerWidth <= 768);
    checkIsMobile();
    window.addEventListener("resize", checkIsMobile);
    return () => window.removeEventListener("resize", checkIsMobile);
  }, []);

  useEffect(() => {
    chatRef.current = chat;
  }, [chat]);

  const storeInfo = chat ? stores?.find(store => store.name === chat.store) : null;

  useEffect(() => {
    if (storeInfo) {
      setSelectedStore(storeInfo);
      storeRef.current = storeInfo;
      const provider = chat?.modelProvider || storeInfo.modelProvider;
      setSelectedProvider(provider);
      providerRef.current = provider;
    }
  }, [storeInfo, chat]);

  useEffect(() => {
    if (!chat || !defaultStore || !defaultStore.childModelProviders || defaultStore.childModelProviders.length === 0) {
      setModelProviders([]);
    } else {
      ProviderBackend.getProviders(chat.owner)
        .then((res) => {
          if (res.status === "ok") {
            const providers = res.data.filter(provider =>
              provider.category === "Model" && defaultStore.childModelProviders.includes(provider.name)
            );
            if (storeInfo?.modelProvider && !providers.some(p => p.name === storeInfo.modelProvider)) {
              const missingProvider = res.data.find(p => p.name === storeInfo.modelProvider && p.category === "Model");
              if (missingProvider) {
                providers.unshift(missingProvider);
              }
            }
            setModelProviders(providers);
          }
        });
    }
  }, [chat, defaultStore, storeInfo]);

  const updateStoreAndChat = async(newStore, newProvider) => {
    if (isUpdating) {return;}
    setIsUpdating(true);
    try {
      const updatedChat = {...chatRef.current};
      let storeChanged = false;
      let providerChanged = false;

      if (newStore && newStore.name !== updatedChat.store) {
        updatedChat.store = newStore.name;
        storeChanged = true;
      }
      if (newProvider !== undefined && newProvider !== updatedChat.modelProvider) {
        updatedChat.modelProvider = newProvider;
        providerChanged = true;
      }

      if (storeChanged || providerChanged) {
        const chatRes = await ChatBackend.updateChat(updatedChat.owner, updatedChat.name, updatedChat);
        if (chatRes.status !== "ok") {
          throw new Error("Failed to update settings");
        }
        if (onChatUpdated) {
          onChatUpdated(updatedChat);
        }
        chatRef.current = updatedChat;
        if (newProvider !== undefined) {
          providerRef.current = newProvider;
          setSelectedProvider(newProvider);
        }
      }
    } catch (error) {
      Setting.showMessage("error", `${i18next.t("general:Failed to save")}: ${error.message}`);
      setSelectedStore(storeRef.current);
      setSelectedProvider(providerRef.current);
    } finally {
      setIsUpdating(false);
    }
  };

  const handleStoreChange = (e) => {
    const value = e.target.value;
    const newStore = stores?.find(store => store.name === value);
    if (newStore && chat) {
      setSelectedStore(newStore);
      if (!chat.modelProvider && newStore.modelProvider) {
        setSelectedProvider(newStore.modelProvider);
      }
      updateStoreAndChat(newStore, newStore.modelProvider);
      if (onStoreChange) {
        const updatedChat = onStoreChange(newStore);
        if (updatedChat) {
          chatRef.current = updatedChat;
        }
      }
    }
  };

  const handleProviderChange = (e) => {
    const value = e.target.value;
    const newProvider = modelProviders.find(provider => provider.name === value);
    if (newProvider && storeInfo) {
      updateStoreAndChat(null, newProvider.name);
    }
  };

  const addPane = () => {
    if (paneCount < 4 && onPaneCountChange) {
      onPaneCountChange(paneCount + 1);
    }
  };

  const deletePane = () => {
    if (paneCount > 1 && onPaneCountChange) {
      onPaneCountChange(paneCount - 1);
    }
  };

  const storeOptions = useMemo(() => {
    if (filteredStores.length > 0) {
      const currentStoreInFiltered = storeInfo && filteredStores.some(store => store.name === storeInfo.name);
      if (!currentStoreInFiltered && storeInfo) {
        return [storeInfo, ...filteredStores];
      }
      return filteredStores;
    }
    return storeInfo ? [storeInfo] : [];
  }, [filteredStores, storeInfo]);

  const canChangeStores = storeOptions.length > 1;
  const shouldShowTitleBar = paneCount === 1 && (storeInfo || modelProviders.length > 0 || (showPaneControls && canManagePanes));

  if (!shouldShowTitleBar) {
    return null;
  }

  return (
    <div className="flex items-center justify-between px-4 py-2.5 border-b border-border">
      <div className="flex items-center gap-5">
        {storeInfo && (
          <div className="flex items-center gap-2">
            {!isMobile && <span className="text-sm text-muted-foreground">{i18next.t("general:Store")}:</span>}
            <select
              value={selectedStore?.name || storeInfo.name}
              onChange={handleStoreChange}
              disabled={isUpdating || !canChangeStores}
              className="h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50"
              style={{width: isMobile ? "35vw" : "12rem"}}
            >
              {storeOptions.map(store => (
                <option key={store.name} value={store.name}>
                  {store.displayName || store.name}
                </option>
              ))}
            </select>
          </div>
        )}

        {modelProviders.length > 0 && (
          <div className="flex items-center gap-2">
            {!isMobile && <span className="text-sm text-muted-foreground">{i18next.t("general:Model")}:</span>}
            <select
              value={selectedProvider || chat?.modelProvider || storeInfo?.modelProvider || (modelProviders[0]?.name)}
              onChange={handleProviderChange}
              disabled={isUpdating}
              className="h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50"
              style={{width: isMobile ? "35vw" : "15rem"}}
            >
              {modelProviders.map(provider => (
                <option key={provider.name} value={provider.name}>
                  {provider.displayName || provider.name}
                </option>
              ))}
            </select>
          </div>
        )}

        {storeInfo?.showAutoRead && (
          <label className="flex items-center gap-2 cursor-pointer">
            <span className="text-sm text-muted-foreground">{i18next.t("store:Auto read")}:</span>
            <input
              type="checkbox"
              checked={autoRead}
              onChange={(e) => onUpdateAutoRead(e.target.checked)}
              className="w-4 h-4 rounded border-border accent-primary"
            />
          </label>
        )}

        {showPaneControls && canManagePanes && (
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">{i18next.t("chat:Panes")}: {paneCount}</span>
            <button onClick={addPane} className="p-1 rounded border border-border text-muted-foreground hover:text-foreground hover:bg-secondary transition-colors">
              <Plus className="w-3 h-3" />
            </button>
            <button onClick={deletePane} disabled={paneCount <= 1} className="p-1 rounded border border-border text-muted-foreground hover:text-foreground hover:bg-secondary disabled:opacity-30 transition-colors">
              <Minus className="w-3 h-3" />
            </button>
          </div>
        )}
      </div>

      {storeInfo && (
        <div className="text-sm text-muted-foreground">
          {storeInfo.type && (
            <span><strong className="text-foreground">Type:</strong> {storeInfo.type}</span>
          )}
          {storeInfo.url && (
            <span className="ml-4">
              <strong className="text-foreground">URL:</strong> {Setting.getShortText(storeInfo.url, 30)}
            </span>
          )}
        </div>
      )}
    </div>
  );
};

export default StoreInfoTitle;
