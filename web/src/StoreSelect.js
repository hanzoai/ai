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

import React, {useCallback, useEffect, useState} from "react";
import i18next from "i18next";
import * as StoreBackend from "./backend/StoreBackend";
import * as Setting from "./Setting";

function StoreSelect({style, onSelect, withAll, className, disabled, account}) {
  const [stores, setStores] = useState([]);
  const [value, setValue] = useState(Setting.getStore());
  const [initialized, setInitialized] = useState(false);

  const handleOnChange = useCallback((val) => {
    setValue(val);
    Setting.setStore(val);
  }, []);

  const getUserBoundStore = useCallback((storeList) => {
    if (account && account.homepage && storeList) {
      const matchingStore = storeList.find(store => store.name === account.homepage);
      if (matchingStore) {
        return matchingStore.name;
      }
    }
    return null;
  }, [account]);

  const getStores = useCallback(() => {
    const currentStore = Setting.getStore();
    if (currentStore) {
      setValue(currentStore);
    }

    StoreBackend.getStoreNames("admin")
      .then((res) => {
        if (res.status === "ok") {
          setStores(res.data);

          const userBoundStore = getUserBoundStore(res.data);
          if (userBoundStore) {
            handleOnChange(userBoundStore);
          } else {
            const selectedValueExist = res.data.filter(store => store.name === value).length > 0;
            if (Setting.getStore() === undefined || !selectedValueExist) {
              const items = res.data;
              if (items.length > 0) {
                handleOnChange(items[0].name);
              }
            }
          }
          setInitialized(true);
        }
      });
  }, [getUserBoundStore, handleOnChange, value]);

  useEffect(() => {
    getStores();
    window.addEventListener("storesChanged", getStores);

    const handleStorageChange = (e) => {
      if (e.storageArea && "store" in e.storageArea) {
        const currentStore = Setting.getStore();
        if (currentStore && currentStore !== value) {
          setValue(currentStore);
        }
      }
    };
    window.addEventListener("storage", handleStorageChange);

    return () => {
      window.removeEventListener("storesChanged", getStores);
      window.removeEventListener("storage", handleStorageChange);
    };
  }, []);

  const isUserBound = getUserBoundStore(stores) !== null;
  const isDisabled = disabled || isUserBound;

  const getStoreItems = () => {
    const items = stores.map(store => ({label: store.displayName, value: store.name}));
    if (withAll) {
      items.unshift({label: i18next.t("store:All"), value: "All"});
    }
    return items;
  };

  if (!initialized) {
    return <div style={{...style, width: "100%", height: "32px"}} className={className}></div>;
  }

  const items = getStoreItems();

  return (
    <select
      value={value}
      onChange={(e) => {
        handleOnChange(e.target.value);
        if (onSelect) {onSelect(e.target.value);}
      }}
      disabled={isDisabled}
      className={`h-8 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50 disabled:cursor-not-allowed ${className || ""}`}
      style={style}
    >
      {items.map((item) => (
        <option key={item.value} value={item.value}>{item.label}</option>
      ))}
    </select>
  );
}

export default StoreSelect;
